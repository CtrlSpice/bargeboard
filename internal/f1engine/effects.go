package f1engine

import (
	"math/big"
	"slices"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

type attrKind uint8

const (
	attrInt attrKind = iota + 1
	attrFloat
	attrString
	attrBool
)

type attrValue struct {
	kind     attrKind
	intVal   int64
	floatVal float64
	strVal   string
	boolVal  bool
}

type keyValue struct {
	key   string
	value attrValue
}

func intAttr(k string, v int64) keyValue { return keyValue{k, attrValue{kind: attrInt, intVal: v}} }
func floatAttr(k string, v float64) keyValue {
	return keyValue{k, attrValue{kind: attrFloat, floatVal: v}}
}
func strAttr(k, v string) keyValue       { return keyValue{k, attrValue{kind: attrString, strVal: v}} }
func boolAttr(k string, v bool) keyValue { return keyValue{k, attrValue{kind: attrBool, boolVal: v}} }

type spanKindType string

const (
	spanDriverSession   spanKindType = "driver.session"
	spanQualifyingPhase spanKindType = "qualifying.phase"
	spanStint           spanKindType = "stint"
	spanLap             spanKindType = "lap"
	spanSector          spanKindType = "sector"
)

type spanStatusCode uint8

const (
	spanStatusUnset spanStatusCode = iota
	spanStatusOK
	spanStatusError
)

type spanStatusEffect struct {
	code    spanStatusCode
	message string
}
type spanEventEffect struct {
	name        string
	timestamp   time.Time
	timeQuality string
	attrs       []keyValue
}
type spanEffect struct {
	kind                                  spanKindType
	identity                              SessionInfoIdentity
	driverNumber                          int64
	driverAcronym, phase                  string
	stintOrdinal, lapNumber, sectorNumber int64
	displayName                           string
	startTime, endTime                    time.Time
	timeQuality                           string
	status                                spanStatusEffect
	attrs                                 []keyValue
	events                                []spanEventEffect
}
type metricKind uint8

const (
	metricIntGauge metricKind = iota + 1
	metricDoubleGauge
	metricIntSumCumulative
	metricIntSumDelta
	metricExplicitHistogramDelta
	metricExpHistogramDelta
)

type exemplarEffect struct {
	exact         *big.Rat
	order         [3]int64
	timestamp     time.Time
	intVal        int64
	floatVal      float64
	isInt         bool
	traceID       pcommon.TraceID
	spanID        pcommon.SpanID
	filteredAttrs []keyValue
}
type metricEffect struct {
	name, description, unit string
	kind                    metricKind
	monotonic               bool
	identity                SessionInfoIdentity
	dataSource              string
	attrs                   []keyValue
	startTime, timestamp    time.Time
	intVal                  int64
	floatVal                float64
	bounds                  []float64
	bucketCounts            []uint64
	count                   uint64
	sum                     float64
	hasMinMax               bool
	min, max                float64
	expScale                int32
	expZeroCount            uint64
	expPositiveOffset       int32
	expPositiveBuckets      []uint64
	exemplars               []exemplarEffect
}
type logEffect struct {
	body                         string
	timestamp, observedTimestamp time.Time
	identity                     SessionInfoIdentity
	dataSource, timeQuality      string
	hasTrace                     bool
	traceID                      pcommon.TraceID
	spanID                       pcommon.SpanID
	attrs                        []keyValue
}
type Effects struct {
	spans   []spanEffect
	metrics []metricEffect
	logs    []logEffect
}

func (e Effects) empty() bool { return len(e.spans)+len(e.metrics)+len(e.logs) == 0 }
func (e Effects) append(other Effects) Effects {
	out := Effects{slices.Concat(e.spans, other.spans), slices.Concat(e.metrics, other.metrics), slices.Concat(e.logs, other.logs)}
	for i := range out.spans {
		s := &out.spans[i]
		s.attrs = slices.Clone(s.attrs)
		s.events = slices.Clone(s.events)
		for j := range s.events {
			s.events[j].attrs = slices.Clone(s.events[j].attrs)
		}
	}
	for i := range out.metrics {
		m := &out.metrics[i]
		m.attrs = slices.Clone(m.attrs)
		m.bounds = slices.Clone(m.bounds)
		m.bucketCounts = slices.Clone(m.bucketCounts)
		m.expPositiveBuckets = slices.Clone(m.expPositiveBuckets)
		m.exemplars = slices.Clone(m.exemplars)
		for j := range m.exemplars {
			m.exemplars[j].filteredAttrs = slices.Clone(m.exemplars[j].filteredAttrs)
			if exact := m.exemplars[j].exact; exact != nil {
				m.exemplars[j].exact = new(big.Rat).Set(exact)
			}
		}
	}
	for i := range out.logs {
		out.logs[i].attrs = slices.Clone(out.logs[i].attrs)
	}
	return out
}

func (e Effects) HasSpans() bool   { return len(e.spans) > 0 }
func (e Effects) HasMetrics() bool { return len(e.metrics) > 0 }
func (e Effects) HasLogs() bool    { return len(e.logs) > 0 }
