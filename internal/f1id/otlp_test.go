package f1id

import (
	"reflect"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestOTLPIdentity(t *testing.T) {
	for _, version := range []string{"", " \t", " v1 "} {
		r := pcommon.NewResource()
		r.Attributes().PutStr("service.instance.id", "deployed")
		InitResource(r, version)
		want := map[string]any{"service.namespace": "github.com/CtrlSpice", "service.name": "bargeboard", "service.instance.id": "deployed"}
		if version == " v1 " {
			want["service.version"] = version
		}
		if got := r.Attributes().AsRaw(); !reflect.DeepEqual(got, want) {
			t.Errorf("resource: %#v", got)
		}
	}
	s := pcommon.NewInstrumentationScope()
	InitScope(s)
	if s.Name() != "github.com/CtrlSpice/bargeboard/f1" || s.Version() != "1.0.0" || s.Attributes().Len() != 0 {
		t.Fatalf("scope %q %q %#v", s.Name(), s.Version(), s.Attributes().AsRaw())
	}
	for _, source := range []string{"livetiming", "openf1"} {
		attrs := pcommon.NewMap()
		PutCommonSessionAttributes(attrs, SessionIdentity{2025, 123, "practice", "practice_1"}, source)
		want := map[string]any{"f1.season.year": int64(2025), "f1.meeting.key": int64(123), "f1.session.type": "practice", "f1.session.name": "practice_1", "f1.data.source": source}
		if got := attrs.AsRaw(); !reflect.DeepEqual(got, want) {
			t.Errorf("attributes: %#v", got)
		}
	}
}
