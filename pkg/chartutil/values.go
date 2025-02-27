/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package chartutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/golang/protobuf/ptypes/timestamp"
	"k8s.io/helm/pkg/proto/hapi/chart"
)

// ErrNoTable indicates that a chart does not have a matching table.
type ErrNoTable error

// ErrNoValue indicates that Values does not contain a key with a value
type ErrNoValue error

// GlobalKey is the name of the Values key that is used for storing global vars.
const GlobalKey = "global"

// Values represents a collection of chart values.
type Values map[string]interface{}

// MergeValues merges source and destination map, preferring values from the source map
func MergeValues(dest Values, src Values) Values {
	for k, v := range src {
		// If the key doesn't exist already, then just set the key to that value
		if _, exists := dest[k]; !exists {
			dest[k] = v
			continue
		}
		nextMap, ok := v.(Values)
		// If it isn't another map, overwrite the value
		if !ok {
			dest[k] = v
			continue
		}
		// Edge case: If the key exists in the destination, but isn't a map
		destMap, isMap := dest[k].(Values)
		// If the source map has a map for this key, prefer it
		if !isMap {
			dest[k] = v
			continue
		}
		// If we got to this point, it is a map in both, so merge them
		dest[k] = MergeValues(destMap, nextMap)
	}
	return dest
}

// YAML encodes the Values into a YAML string.
func (v Values) YAML() (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// Table gets a table (YAML subsection) from a Values object.
//
// The table is returned as a Values.
//
// Compound table names may be specified with dots:
//
//	foo.bar
//
// The above will be evaluated as "The table bar inside the table
// foo".
//
// An ErrNoTable is returned if the table does not exist.
func (v Values) Table(name string) (Values, error) {
	names := strings.Split(name, ".")
	table := v
	var err error

	for _, n := range names {
		table, err = tableLookup(table, n)
		if err != nil {
			return table, err
		}
	}
	return table, err
}

// AsMap is a utility function for converting Values to a map[string]interface{}.
//
// It protects against nil map panics.
func (v Values) AsMap() map[string]interface{} {
	if v == nil || len(v) == 0 {
		return map[string]interface{}{}
	}
	return v
}

// Encode writes serialized Values information to the given io.Writer.
func (v Values) Encode(w io.Writer) error {
	//return yaml.NewEncoder(w).Encode(v)
	out, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// MergeInto takes the properties in src and merges them into Values. Maps
// are merged while values and arrays are replaced.
func (v Values) MergeInto(src Values) {
	for key, srcVal := range src {
		destVal, found := v[key]

		if found && istable(srcVal) && istable(destVal) {
			destMap := destVal.(map[string]interface{})
			srcMap := srcVal.(map[string]interface{})
			Values(destMap).MergeInto(Values(srcMap))
		} else {
			v[key] = srcVal
		}
	}
}

func tableLookup(v Values, simple string) (Values, error) {
	v2, ok := v[simple]
	if !ok {
		return v, ErrNoTable(fmt.Errorf("no table named %q (%v)", simple, v))
	}
	if vv, ok := v2.(map[string]interface{}); ok {
		return vv, nil
	}

	// This catches a case where a value is of type Values, but doesn't (for some
	// reason) match the map[string]interface{}. This has been observed in the
	// wild, and might be a result of a nil map of type Values.
	if vv, ok := v2.(Values); ok {
		return vv, nil
	}

	var e ErrNoTable = fmt.Errorf("no table named %q", simple)
	return map[string]interface{}{}, e
}

// ReadValues will parse YAML byte data into a Values.
func ReadValues(data []byte) (vals Values, err error) {
	err = yaml.Unmarshal(data, &vals, func(d *json.Decoder) *json.Decoder {
		d.UseNumber()
		return d
	})
	if len(vals) == 0 {
		vals = Values{}
	}
	return
}

// ReadValuesFile will parse a YAML file into a map of values.
func ReadValuesFile(filename string) (Values, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return map[string]interface{}{}, err
	}
	return ReadValues(data)
}

// CoalesceValues coalesces all of the values in a chart (and its subcharts).
//
// Values are coalesced together using the following rules:
//
//	- Values in a higher level chart always override values in a lower-level
//		dependency chart
//	- Scalar values and arrays are replaced, maps are merged
//	- A chart has access to all of the variables for it, as well as all of
//		the values destined for its dependencies.
func CoalesceValues(chrt *chart.Chart, vals *chart.Config) (Values, error) {
	cvals := Values{}
	// Parse values if not nil. We merge these at the top level because
	// the passed-in values are in the same namespace as the parent chart.
	if vals != nil {
		evals, err := ReadValues([]byte(vals.Raw))
		if err != nil {
			return cvals, err
		}
		return coalesce(chrt, evals)
	}

	return coalesceDeps(chrt, cvals)
}

// coalesce coalesces the dest values and the chart values, giving priority to the dest values.
//
// This is a helper function for CoalesceValues.
func coalesce(ch *chart.Chart, dest map[string]interface{}) (map[string]interface{}, error) {
	var err error
	dest, err = coalesceValues(ch, dest)
	if err != nil {
		return dest, err
	}
	return coalesceDeps(ch, dest)
}

// coalesceDeps coalesces the dependencies of the given chart.
func coalesceDeps(chrt *chart.Chart, dest map[string]interface{}) (map[string]interface{}, error) {
	for _, subchart := range chrt.Dependencies {
		if c, ok := dest[subchart.Metadata.Name]; !ok {
			// If dest doesn't already have the key, create it.
			dest[subchart.Metadata.Name] = map[string]interface{}{}
		} else if !istable(c) {
			return dest, fmt.Errorf("type mismatch on %s: %t", subchart.Metadata.Name, c)
		}
		if dv, ok := dest[subchart.Metadata.Name]; ok {
			dvmap := dv.(map[string]interface{})

			// Get globals out of dest and merge them into dvmap.
			dvmap = coalesceGlobals(dvmap, dest, chrt.Metadata.Name)

			var err error
			// Now coalesce the rest of the values.
			dest[subchart.Metadata.Name], err = coalesce(subchart, dvmap)
			if err != nil {
				return dest, err
			}
		}
	}
	return dest, nil
}

// coalesceGlobals copies the globals out of src and merges them into dest.
//
// For convenience, returns dest.
func coalesceGlobals(dest, src map[string]interface{}, chartName string) map[string]interface{} {
	var dg, sg map[string]interface{}

	if destglob, ok := dest[GlobalKey]; !ok {
		dg = map[string]interface{}{}
	} else if dg, ok = destglob.(map[string]interface{}); !ok {
		log.Printf("Warning: Skipping globals for chart '%s' because destination '%s' is not a table.", chartName, GlobalKey)
		return dg
	}

	if srcglob, ok := src[GlobalKey]; !ok {
		sg = map[string]interface{}{}
	} else if sg, ok = srcglob.(map[string]interface{}); !ok {
		log.Printf("Warning: skipping globals for chart '%s' because source '%s' is not a table.", chartName, GlobalKey)
		return dg
	}

	rv := make(map[string]interface{})
	for k, v := range dest {
		rv[k] = v
	}

	// EXPERIMENTAL: In the past, we have disallowed globals to test tables. This
	// reverses that decision. It may somehow be possible to introduce a loop
	// here, but I haven't found a way. So for the time being, let's allow
	// tables in globals.

	// Basically, we reverse order of coalesce here to merge
	// top-down.
	rv[GlobalKey] = coalesceTables(sg, dg, chartName)
	return rv
}

// coalesceValues builds up a values map for a particular chart.
//
// Values in v will override the values in the chart.
func coalesceValues(c *chart.Chart, v map[string]interface{}) (map[string]interface{}, error) {
	// If there are no values in the chart, we just return the given values
	if c.Values == nil || c.Values.Raw == "" {
		return v, nil
	}

	nv, err := ReadValues([]byte(c.Values.Raw))
	if err != nil {
		// On error, we return just the overridden values.
		// FIXME: We should log this error. It indicates that the YAML data
		// did not parse.
		return v, fmt.Errorf("Error: Reading chart '%s' default values (%s): %s", c.Metadata.Name, c.Values.Raw, err)
	}

	return coalesceTables(v, nv.AsMap(), c.Metadata.Name), nil
}

// coalesceTables merges a source map into a destination map.
//
// dest is considered authoritative.
func coalesceTables(dst, src map[string]interface{}, chartName string) map[string]interface{} {
	// Because dest has higher precedence than src, dest values override src
	// values.

	rv := make(map[string]interface{})
	for key, val := range src {
		dv, ok := dst[key]
		if !ok { // if not in dst, then copy from src
			rv[key] = val
			continue
		}
		if dv == nil { // if set to nil in dst, then ignore
			// When the YAML value is null, we skip the value's key.
			// This allows Helm's various sources of values (value files or --set) to
			// remove incompatible keys from any previous chart, file, or set values.
			continue
		}

		srcTable, srcIsTable := val.(map[string]interface{})
		dstTable, dstIsTable := dv.(map[string]interface{})
		switch {
		case srcIsTable && dstIsTable: // both tables, we coalesce
			rv[key] = coalesceTables(dstTable, srcTable, chartName)
		case srcIsTable && !dstIsTable:
			log.Printf("Warning: Merging destination map for chart '%s'. Overwriting table item '%s', with non table value: %v", chartName, key, dv)
			rv[key] = dv
		case !srcIsTable && dstIsTable:
			log.Printf("Warning: Merging destination map for chart '%s'. The destination item '%s' is a table and ignoring the source '%s' as it has a non-table value of: %v", chartName, key, key, val)
			rv[key] = dv
		default: // neither are tables, simply take the dst value
			rv[key] = dv
		}
	}

	// do we have anything in dst that wasn't processed already that we need to copy across?
	for key, val := range dst {
		if val == nil {
			continue
		}
		_, ok := rv[key]
		if !ok {
			rv[key] = val
		}
	}

	return rv
}

// ReleaseOptions represents the additional release options needed
// for the composition of the final values struct
type ReleaseOptions struct {
	Name      string
	Time      *timestamp.Timestamp
	Namespace string
	IsUpgrade bool
	IsInstall bool
	Revision  int
}

// ToRenderValues composes the struct from the data coming from the Releases, Charts and Values files
//
// WARNING: This function is deprecated for Helm > 2.1.99 Use ToRenderValuesCaps() instead. It will
// remain in the codebase to stay SemVer compliant.
//
// In Helm 3.0, this will be changed to accept Capabilities as a fourth parameter.
func ToRenderValues(chrt *chart.Chart, chrtVals *chart.Config, options ReleaseOptions) (Values, error) {
	caps := &Capabilities{APIVersions: DefaultVersionSet}
	return ToRenderValuesCaps(chrt, chrtVals, options, caps)
}

// ToRenderValuesCaps composes the struct from the data coming from the Releases, Charts and Values files
//
// This takes both ReleaseOptions and Capabilities to merge into the render values.
func ToRenderValuesCaps(chrt *chart.Chart, chrtVals *chart.Config, options ReleaseOptions, caps *Capabilities) (Values, error) {

	top := map[string]interface{}{
		"Release": map[string]interface{}{
			"Name":      options.Name,
			"Time":      options.Time,
			"Namespace": options.Namespace,
			"IsUpgrade": options.IsUpgrade,
			"IsInstall": options.IsInstall,
			"Revision":  options.Revision,
			"Service":   "Tiller",
		},
		"Chart":        chrt.Metadata,
		"Files":        NewFiles(chrt.Files),
		"Capabilities": caps,
	}

	vals, err := CoalesceValues(chrt, chrtVals)
	if err != nil {
		return top, err
	}

	top["Values"] = vals
	return top, nil
}

// istable is a special-purpose function to see if the present thing matches the definition of a YAML table.
func istable(v interface{}) bool {
	_, ok := v.(map[string]interface{})
	return ok
}

// PathValue takes a path that traverses a YAML structure and returns the value at the end of that path.
// The path starts at the root of the YAML structure and is comprised of YAML keys separated by periods.
// Given the following YAML data the value at path "chapter.one.title" is "Loomings".
//
//	chapter:
//	  one:
//	    title: "Loomings"
func (v Values) PathValue(ypath string) (interface{}, error) {
	if len(ypath) == 0 {
		return nil, errors.New("YAML path string cannot be zero length")
	}
	yps := strings.Split(ypath, ".")
	if len(yps) == 1 {
		// if exists must be root key not table
		vals := v.AsMap()
		k := yps[0]
		if _, ok := vals[k]; ok && !istable(vals[k]) {
			// key found
			return vals[yps[0]], nil
		}
		// key not found
		return nil, ErrNoValue(fmt.Errorf("%v is not a value", k))
	}
	// join all elements of YAML path except last to get string table path
	ypsLen := len(yps)
	table := yps[:ypsLen-1]
	st := strings.Join(table, ".")
	// get the last element as a string key
	key := yps[ypsLen-1:]
	sk := string(key[0])
	// get our table for table path
	t, err := v.Table(st)
	if err != nil {
		//no table
		return nil, ErrNoValue(fmt.Errorf("%v is not a value", sk))
	}
	// check table for key and ensure value is not a table
	if k, ok := t[sk]; ok && !istable(k) {
		// key found
		return k, nil
	}

	// key not found
	return nil, ErrNoValue(fmt.Errorf("key not found: %s", sk))
}
