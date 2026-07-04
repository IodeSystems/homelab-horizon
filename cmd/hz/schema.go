package main

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runSchema dumps the JSON request schema for a resource by reflecting over the
// shared apitypes structs, so it can never drift from what the server accepts.
func runSchema(args []string) error {
	target := "service"
	if len(args) > 0 {
		target = args[0]
	}
	switch target {
	case "service", "service-create", "service-edit":
		fmt.Printf("ServiceRequest (POST /api/v1/services/add and /edit)\n")
		fmt.Printf("  edit additionally requires \"originalName\" and only differs by that field.\n\n")
		printSchema(reflect.TypeOf(apitypes.ServiceRequest{}), 0, map[reflect.Type]bool{})
		return nil
	default:
		return fmt.Errorf("unknown schema %q (known: service)", target)
	}
}

func printSchema(t reflect.Type, depth int, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	indent := strings.Repeat("  ", depth)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		opt := ""
		if strings.Contains(tag, "omitempty") || f.Type.Kind() == reflect.Pointer {
			opt = "  (optional)"
		}
		ft := f.Type
		isSlice := ft.Kind() == reflect.Slice
		elem := ft
		for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct {
			suffix := ""
			if isSlice {
				suffix = "[]"
			}
			fmt.Printf("%s%s: {%s}%s\n", indent, name, suffix, opt)
			if !seen[elem] {
				seen[elem] = true
				printSchema(elem, depth+1, seen)
				delete(seen, elem)
			}
			continue
		}
		typeName := jsonType(ft)
		fmt.Printf("%s%s: %s%s\n", indent, name, typeName, opt)
	}
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return jsonType(t.Elem())
	case reflect.Slice:
		return jsonType(t.Elem()) + "[]"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "number"
	default:
		return t.Kind().String()
	}
}
