/*
	The MIT License (MIT)

	Copyright 2015-2017 Daniel Nichter

	Permission is hereby granted, free of charge, to any person obtaining a copy
	of this software and associated documentation files (the "Software"), to deal
	in the Software without restriction, including without limitation the rights
	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
	copies of the Software, and to permit persons to whom the Software is
	furnished to do so, subject to the following conditions:

	The above copyright notice and this permission notice shall be included in all
	copies or substantial portions of the Software.

	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
	SOFTWARE.
*/

// Package deep provides function deep.Equal which is like reflect.DeepEqual but
// retunrs a list of differences. This is helpful when comparing complex types
// like structures and maps.
package deep

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
)

var (
	FloatPrecision          = 10
	MaxDiff                 = 10
	MaxDepth                = 10
	LogErrors               = false
	CompareUnexportedFields = false
)

var (
	ErrMaxRecursion = errors.New("recursed to MaxDepth")
	ErrTypeMismatch = errors.New("variables are different reflect.Type")
	ErrNotHandled   = errors.New("cannot compare the reflect.Kind")
)

type cmp struct {
	diff        []string
	buff        []string
	floatFormat string
}

// Equal compares variables a and b, recursing into their structure up to
// MaxDepth levels deep, and returns a list of differences, or nil if there are
// none. Some differences may not be found if an error is also returned.
//
// If a type has an Equal method, like time.Equal, it is called to check for
// equality.
func Equal(a, b interface{}) []string {
	aVal := reflect.ValueOf(a)
	bVal := reflect.ValueOf(b)
	c := &cmp{
		diff:        []string{},
		buff:        []string{},
		floatFormat: fmt.Sprintf("%%.%df", FloatPrecision),
	}
	c.equals(aVal, bVal, 0)
	if len(c.diff) > 0 {
		return c.diff // diffs
	}
	return nil // no diffs
}

func (c *cmp) equals(a, b reflect.Value, level int) {
	if level > MaxDepth {
		logError(ErrMaxRecursion)
		return
	}

	aType := a.Type()
	bType := b.Type()
	if aType != bType {
		c.saveDiff(aType, bType)
		logError(ErrTypeMismatch)
		return
	}

	aKind := a.Kind()
	bKind := b.Kind()
	if aKind == reflect.Ptr || aKind == reflect.Interface {
		a = a.Elem()
		aKind = a.Kind()
		if a.IsValid() {
			aType = a.Type()
		}
	}
	if bKind == reflect.Ptr || bKind == reflect.Interface {
		b = b.Elem()
		bKind = b.Kind()
		if b.IsValid() {
			bType = b.Type()
		}
	}

	// For example: T{x: *X} and T.x is nil.
	if !a.IsValid() || !b.IsValid() {
		if a.IsValid() && !b.IsValid() {
			c.saveDiff(aType, "<nil pointer>")
		} else if !a.IsValid() && b.IsValid() {
			c.saveDiff("<nil pointer>", bType)
		}
		return
	}

	// Types with an Equal(), like time.Time.
	eqFunc := a.MethodByName("Equal")
	if eqFunc.IsValid() {
		retVals := eqFunc.Call([]reflect.Value{b})
		if !retVals[0].Bool() {
			c.saveDiff(a, b)
		}
		return
	}

	switch aKind {

	/////////////////////////////////////////////////////////////////////
	// Iterable kinds
	/////////////////////////////////////////////////////////////////////

	case reflect.Struct:
		/*
			The variables are structs like:
				type T struct {
					FirstName string
					LastName  string
				}
			Type = <pkg>.T, Kind = reflect.Struct

			Iterate through the fields (FirstName, LastName), recurse into their values.
		*/
		for i := 0; i < a.NumField(); i++ {
			if aType.Field(i).PkgPath != "" && !CompareUnexportedFields {
				continue // skip unexported field, e.g. s in type T struct {s string}
			}

			c.push(aType.Field(i).Name) // push field name to buff

			// Get the Value for each field, e.g. FirstName has Type = string,
			// Kind = reflect.String.
			af := a.Field(i)
			bf := b.Field(i)

			// Recurse to compare the field values
			c.equals(af, bf, level+1)

			c.pop() // pop field name from buff

			if len(c.diff) >= MaxDiff {
				break
			}
		}
	case reflect.Map:
		/*
			The variables are maps like:
				map[string]int{
					"foo": 1,
					"bar": 2,
				}
			Type = map[string]int, Kind = reflect.Map

			Or:
				type T map[string]int{}
			Type = <pkg>.T, Kind = reflect.Map

			Iterate through the map keys (foo, bar), recurse into their values.
		*/

		if a.IsNil() || b.IsNil() {
			if a.IsNil() && !b.IsNil() {
				c.saveDiff("<nil map>", b)
			} else if !a.IsNil() && b.IsNil() {
				c.saveDiff(a, "<nil map>")
			}
			return
		}

		if a.Pointer() == b.Pointer() {
			return
		}

		for _, key := range a.MapKeys() {
			c.push(fmt.Sprintf("map[%s]", key))

			aVal := a.MapIndex(key)
			bVal := b.MapIndex(key)
			if bVal.IsValid() {
				c.equals(aVal, bVal, level+1)
			} else {
				c.saveDiff(aVal, "<does not have key>")
			}

			c.pop()

			if len(c.diff) >= MaxDiff {
				return
			}
		}

		for _, key := range b.MapKeys() {
			if aVal := a.MapIndex(key); aVal.IsValid() {
				continue
			}

			c.push(fmt.Sprintf("map[%s]", key))
			c.saveDiff("<does not have key>", b.MapIndex(key))
			c.pop()
			if len(c.diff) >= MaxDiff {
				return
			}
		}
	case reflect.Slice:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() && !b.IsNil() {
				c.saveDiff("<nil slice>", b)
			} else if !a.IsNil() && b.IsNil() {
				c.saveDiff(a, "<nil slice>")
			}
			return
		}

		if a.Pointer() == b.Pointer() {
			return
		}

		aLen := a.Len()
		bLen := b.Len()
		n := aLen
		if bLen > aLen {
			n = bLen
		}
		for i := 0; i < n; i++ {
			c.push(fmt.Sprintf("slice[%d]", i))
			if i < aLen && i < bLen {
				c.equals(a.Index(i), b.Index(i), level+1)
			} else if i < aLen {
				c.saveDiff(a.Index(i), "<no value>")
			} else {
				c.saveDiff("<no value>", b.Index(i))
			}
			c.pop()
			if len(c.diff) >= MaxDiff {
				break
			}
		}

	/////////////////////////////////////////////////////////////////////
	// Primitive kinds
	/////////////////////////////////////////////////////////////////////

	case reflect.Float32, reflect.Float64:
		// Avoid 0.04147685731961082 != 0.041476857319611
		// 6 decimal places is close enough
		aval := fmt.Sprintf(c.floatFormat, a.Float())
		bval := fmt.Sprintf(c.floatFormat, b.Float())
		if aval != bval {
			c.saveDiff(a.Float(), b.Float())
		}
	case reflect.Bool:
		if a.Bool() != b.Bool() {
			c.saveDiff(a.Bool(), b.Bool())
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if a.Int() != b.Int() {
			c.saveDiff(a.Int(), b.Int())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if a.Uint() != b.Uint() {
			c.saveDiff(a.Uint(), b.Uint())
		}
	case reflect.String:
		if a.String() != b.String() {
			c.saveDiff(a.String(), b.String())
		}

	default:
		logError(ErrNotHandled)
	}
}

func (c *cmp) push(name string) {
	c.buff = append(c.buff, name)
}

func (c *cmp) pop() {
	if len(c.buff) > 0 {
		c.buff = c.buff[0 : len(c.buff)-1]
	}
}

func (c *cmp) saveDiff(aval, bval interface{}) {
	if len(c.buff) > 0 {
		varName := strings.Join(c.buff, ".")
		c.diff = append(c.diff, fmt.Sprintf("%s: %v != %v", varName, aval, bval))
	} else {
		c.diff = append(c.diff, fmt.Sprintf("%v != %v", aval, bval))
	}
}

func init() {
	log.SetFlags(log.Lshortfile)
}

func logError(err error) {
	if LogErrors {
		log.Println(err)
	}
}
