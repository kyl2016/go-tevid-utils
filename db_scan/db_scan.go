package db_scan

import (
	"errors"
	"reflect"
	"strconv"
	"time"
)

var (
	ErrTargetNotSettable    = errors.New("目标对象无法进行设值操作")
	ErrConvertValue         = errors.New("值类型转换失败")
	ErrUnSupportTypeConvert = errors.New("暂不支持的类型转换")
	ErrSliceToString        = errors.New("slice转string失败")
	ErrEmptyResult          = errors.New("结果为空")
)

const (
	DefaultTagName    = "pg"                  //默认标签名称
	DefaultTimeFormat = "2006-01-02 15:04:05" //默认时间格式
)

//database/sql的rows抽象接口
type IRows interface {
	Close() error
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...interface{}) error
}

//获取普通对象的类型
func getObjectType(obj interface{}) reflect.Type {
	return reflect.TypeOf(obj)
}

//获取普通对象的值
func getObjectValue(obj interface{}) reflect.Value {
	return reflect.ValueOf(obj)
}

//获取指针对象的值
func getPtrObjectValue(obj interface{}) reflect.Value {
	return reflect.ValueOf(obj).Elem()
}

//获取指针对象的类型
func getPtrObjectType(obj interface{}) reflect.Type {
	return reflect.TypeOf(obj).Elem()
}

func isFloat(k reflect.Kind) bool {
	return k == reflect.Float32 || k == reflect.Float64
}

func isInteger(k reflect.Kind) bool {
	return isSignedInteger(k) || isUnsignedInteger(k)
}

func isSignedInteger(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Int64
}

func isUnsignedInteger(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

//数据集扫描
func Scan(rows IRows, target interface{}) error {
	if nil == target || getObjectValue(target).IsNil() || getObjectType(target).Kind() != reflect.Ptr {
		return ErrTargetNotSettable
	}
	datas, err := ExtraDatasFromRows(rows)
	if nil != err {
		return err
	}
	switch getPtrObjectType(target).Kind() {
	case reflect.Slice:
		if nil == datas {
			return nil
		}
		err = multiResults(datas, target)
	default:
		if nil == datas {
			return ErrEmptyResult
		}
		err = singleResult(datas[0], target)
	}
	return err
}

func ExtraDatasFromRows(rows IRows) ([]map[string]interface{}, error) {
	var result []map[string]interface{}
	columns, err := rows.Columns()
	if nil != err {
		return nil, err
	}
	length := len(columns)
	values := make([]interface{}, length)
	for i := 0; i < length; i++ {
		values[i] = new(interface{})
	}

	for rows.Next() {
		err = rows.Scan(values...)
		if nil != err {
			return nil, err
		}
		mp := make(map[string]interface{})
		for idx, name := range columns {
			mp[name] = *(values[idx].(*interface{}))
		}
		result = append(result, mp)
	}
	return result, nil
}

//多结果集处理
func multiResults(arr []map[string]interface{}, target interface{}) error {
	valueObj := getPtrObjectValue(target)
	if !valueObj.CanSet() {
		return ErrTargetNotSettable
	}

	length := len(arr)
	valueSliceObj := reflect.MakeSlice(valueObj.Type(), 0, length)
	typeObj := valueSliceObj.Type()
	var err error
	for i := 0; i < length; i++ {
		target := reflect.New(typeObj.Elem())
		err = singleResult(arr[i], target.Interface())
		if nil != err {
			return err
		}
		valueSliceObj = reflect.Append(valueSliceObj, target.Elem())
	}
	valueObj.Set(valueSliceObj)
	return nil
}

//单一结果处理
func singleResult(result map[string]interface{}, target interface{}) (resp error) {

	valueObj := getPtrObjectValue(target)
	if !valueObj.CanSet() {
		return ErrTargetNotSettable
	}

	typeObj := getPtrObjectType(target)
	kind := typeObj.Kind()

	//需递归知道获取真实类型位置
	if kind == reflect.Ptr {
		targetInstance := reflect.New(typeObj.Elem())
		err := singleResult(result, targetInstance.Interface())
		if nil == err {
			valueObj.Set(targetInstance)
		}
		return err
	}

	for i := 0; i < valueObj.NumField(); i++ {
		fieldTypeI := typeObj.Field(i)

		valueI := valueObj.Field(i)
		if !valueI.CanSet() {
			continue
		}
		tagName, ok := fieldTypeI.Tag.Lookup(DefaultTagName)
		if !ok || tagName == "" {
			continue
		}
		mapValue, ok := result[tagName]
		if !ok {
			continue
		}
		err := valueConvert(mapValue, valueI)
		if err != nil {
			return err
		}
	}
	return nil
}

//直接设置
func directSet(sourceVal interface{}, rTargetVal reflect.Value) bool {
	sourceType := reflect.TypeOf(sourceVal)
	if nil == sourceType {
		return true
	}

	targetType := rTargetVal.Type()
	//类型刚好匹配
	if sourceType.AssignableTo(targetType) {
		rTargetVal.Set(reflect.ValueOf(sourceVal))
		return true
	}
	return false
}

//map自动数据格式转换
func valueConvert(sourceVal interface{}, rTargetVal reflect.Value) error {

	sourceType := reflect.TypeOf(sourceVal)
	if nil == sourceType {
		return nil
	}
	targetType := rTargetVal.Type()

	if directSet(sourceVal, rTargetVal) {
		return nil
	}

	switch assertT := sourceVal.(type) {
	case time.Time:
		return handleConvertTime(assertT, sourceType, &rTargetVal)
	}

	switch sourceType.Kind() {
	case reflect.Slice:
		return handleConvertMapSliceToField(sourceVal, &rTargetVal)
	case reflect.Int64:
		if isSignedInteger(targetType.Kind()) {
			rTargetVal.SetInt(sourceVal.(int64))
		} else if isUnsignedInteger(targetType.Kind()) {
			rTargetVal.SetUint(uint64(sourceVal.(int64)))
		}
	case reflect.Float32:
		if isFloat(targetType.Kind()) {
			rTargetVal.SetFloat(float64(sourceVal.(float32)))
		}
	case reflect.Float64:
		if isFloat(targetType.Kind()) {
			rTargetVal.SetFloat(sourceVal.(float64))
		}
	default:
		return ErrConvertValue
	}
	return nil
}

//slice的值转换
func handleConvertMapSliceToField(mapValue interface{}, rTargetValPtr *reflect.Value) error {
	rTargetValKind := (*rTargetValPtr).Type().Kind()

	mapValueSlice, ok := mapValue.([]byte)
	if !ok {
		return ErrSliceToString
	}
	mapValueStr := string(mapValueSlice)
	switch {
	case rTargetValKind == reflect.String:
		rTargetValPtr.SetString(mapValueStr)
	case isSignedInteger(rTargetValKind):
		intVal, err := strconv.ParseInt(mapValueStr, 10, 64)
		if nil != err {
			return ErrConvertValue
		}
		rTargetValPtr.SetInt(intVal)
	case isUnsignedInteger(rTargetValKind):
		uintVal, err := strconv.ParseUint(mapValueStr, 10, 64)
		if nil != err {
			return ErrConvertValue
		}
		rTargetValPtr.SetUint(uintVal)
	case isFloat(rTargetValKind):
		floatVal, err := strconv.ParseFloat(mapValueStr, 64)
		if nil != err {
			return ErrConvertValue
		}
		rTargetValPtr.SetFloat(floatVal)
	default:
		return ErrUnSupportTypeConvert
	}
	return nil
}

func handleConvertTime(assertT time.Time, mvt reflect.Type, valueI *reflect.Value) error {
	if (*valueI).Type().Kind() == reflect.String {
		str := assertT.Format(DefaultTimeFormat)
		valueI.SetString(str)
		return nil
	}
	return ErrConvertValue
}
