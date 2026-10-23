package destination

import (
	"reflect"
	"testing"

	"ikhnos/internal/store"
)

// Of and ToStore are written out field by field. A destination that has every field set must come back as it went in, so a field added to
// the store's type and left out of the document fails here and not in somebody's saved intent.
func TestADestinationSurvivesTheDocument(t *testing.T) {
	var d store.Destination
	v := reflect.ValueOf(&d).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x" + v.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(true)
		default:
			t.Fatalf("store.Destination.%s is a %s: teach this test and the document what to do with it", v.Type().Field(i).Name, f.Kind())
		}
	}
	if got := Of(d).ToStore(); got != d {
		t.Fatalf("a destination changed on its way through the document:\n got %+v\nwant %+v", got, d)
	}
}
