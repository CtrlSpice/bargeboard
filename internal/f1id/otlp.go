package f1id

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

const (
	ScopeName        = "github.com/CtrlSpice/bargeboard/f1"
	ScopeVersion     = "1.0.0"
	ServiceNamespace = "github.com/CtrlSpice"
	ServiceName      = "bargeboard"
)

func InitResource(dest pcommon.Resource, buildVersion string) {
	attrs := dest.Attributes()
	attrs.PutStr("service.namespace", ServiceNamespace)
	attrs.PutStr("service.name", ServiceName)
	if strings.TrimSpace(buildVersion) != "" {
		attrs.PutStr("service.version", buildVersion)
	}
}

func InitScope(dest pcommon.InstrumentationScope) {
	dest.SetName(ScopeName)
	dest.SetVersion(ScopeVersion)
}

func PutCommonSessionAttributes(attrs pcommon.Map, e SessionIdentity, dataSource string) {
	attrs.PutInt("f1.season.year", e.Season)
	attrs.PutInt("f1.meeting.key", e.MeetingKey)
	attrs.PutStr("f1.session.type", string(e.SessionType))
	attrs.PutStr("f1.session.name", string(e.SessionName))
	attrs.PutStr("f1.data.source", dataSource)
}
