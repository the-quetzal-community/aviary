package community

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"runtime.link/api"
)

func SendVia(send chan<- []byte) *Log {
	var log Log
	for fn := range api.StructureOf(&log).Iter() {
		fn.Make(reflect.MakeFunc(fn.Type, func(args []reflect.Value) []reflect.Value {
			var w bytes.Buffer
			w.WriteString(`{"`)
			w.WriteString(fn.Name)
			w.WriteString(`":[`)
			encoder := json.NewEncoder(&w)
			for i, arg := range args {
				if i > 0 {
					w.WriteString(",")
				}
				if err := encoder.Encode(arg.Interface()); err != nil {
					panic(err)
				}
			}
			w.WriteString("]}")
			send <- w.Bytes()
			return nil
		}))
	}
	return &log
}

func SendTo(send func([]byte)) *Log {
	var log Log
	for fn := range api.StructureOf(&log).Iter() {
		fn.Make(reflect.MakeFunc(fn.Type, func(args []reflect.Value) []reflect.Value {
			var w bytes.Buffer
			w.WriteString(`{"`)
			w.WriteString(fn.Name)
			w.WriteString(`":[`)
			encoder := json.NewEncoder(&w)
			for i, arg := range args {
				if i > 0 {
					w.WriteString(",")
				}
				if err := encoder.Encode(arg.Interface()); err != nil {
					panic(err)
				}
			}
			w.WriteString("]}")
			send(w.Bytes())
			return nil
		}))
	}
	return &log
}

func ProcessSingle(packet []byte, into *Log) bool {
	var functions = make(map[string]reflect.Value)
	for fn := range api.StructureOf(into).Iter() {
		functions[fn.Name] = fn.Impl
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(packet, &data); err != nil {
		if err == io.EOF {
			return true
		}
		return false
	}
	for name, value := range data {
		fn, ok := functions[name]
		if !ok {
			return false
		}
		var args = make([]json.RawMessage, 0, len(value))
		if err := json.Unmarshal(value, &args); err != nil {
			fmt.Println("error unmarshalling arguments for", name, ":", err)
			return false
		}
		if len(args) != fn.Type().NumIn() {
			fmt.Println("argument count mismatch for", name, "expected", fn.Type().NumIn(), "got", len(args))
			return false
		}
		var converted = make([]reflect.Value, len(args))
		for i, arg := range args {
			converted[i] = reflect.New(fn.Type().In(i)).Elem()
			if err := json.Unmarshal(arg, converted[i].Addr().Interface()); err != nil {
				fmt.Println("error unmarshalling argument", i, "for", name, ":", err)
				return false
			}
		}
		fn.Call(converted)
	}
	return true
}

func Process(recv <-chan []byte, into *Log) {
	var functions = make(map[string]reflect.Value)
	for fn := range api.StructureOf(into).Iter() {
		functions[fn.Name] = fn.Impl
	}
serving:
	for {
		packet, ok := <-recv
		if !ok {
			return
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(packet, &data); err != nil {
			if err == io.EOF {
				return
			}
			panic(err)
		}
		for name, value := range data {
			fn, ok := functions[name]
			if !ok {
				continue
			}
			var args = make([]json.RawMessage, 0, len(value))
			if err := json.Unmarshal(value, &args); err != nil {
				fmt.Println("error unmarshalling arguments for", name, ":", err)
				continue
			}
			if len(args) != fn.Type().NumIn() {
				fmt.Println("argument count mismatch for", name, "expected", fn.Type().NumIn(), "got", len(args))
				continue
			}
			var converted = make([]reflect.Value, len(args))
			for i, arg := range args {
				converted[i] = reflect.New(fn.Type().In(i)).Elem()
				if err := json.Unmarshal(arg, converted[i].Addr().Interface()); err != nil {
					fmt.Println("error unmarshalling argument", i, "for", name, ":", err)
					continue serving
				}
			}
			fn.Call(converted)
		}
	}
}
