package repository

import (
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// UpdatePHPSettings writes every field, so a partial update that starts from
// DomainPHPSettingsOf keeps what it does not change only if every field is
// copied. With every PHP pointer set on the domain, no setting may come back
// nil: a field added to DomainPHPSettings but not copied here would be
// cleared by `jabali domain php-settings set` (GH #1701 Slice 3: an admin's
// open_basedir among them).
func TestDomainPHPSettingsOf_CopiesEverySetting(t *testing.T) {
	var d models.Domain
	dv := reflect.ValueOf(&d).Elem()
	for i := 0; i < dv.NumField(); i++ {
		f := dv.Type().Field(i)
		if strings.HasPrefix(f.Name, "PHP") && f.Type.Kind() == reflect.Pointer {
			dv.Field(i).Set(reflect.New(f.Type.Elem()))
		}
	}
	s := reflect.ValueOf(DomainPHPSettingsOf(&d))
	for i := 0; i < s.NumField(); i++ {
		if s.Field(i).IsNil() {
			t.Errorf("DomainPHPSettingsOf does not copy %s", s.Type().Field(i).Name)
		}
	}
}
