package f1livetimingreceiver

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReduceDriverRegistryStagesSparseFeedWithoutFreezing(t *testing.T) {
	state := driverRegistryState{}
	steps := []struct {
		name    string
		payload string
		want    driverRegistryState
		issues  driverListIssueSet
	}{
		{
			name:    "acronym starts staging",
			payload: `{"44":{"Tla":"AAA"}}`,
			want: driverRegistryTestState(driverRegistryEntry{
				number: 44, tla: "AAA", tlaState: driverListFieldValid,
			}),
		},
		{
			name:    "matching number enriches staging",
			payload: `{"44":{"RacingNumber":"44"}}`,
			want: driverRegistryTestState(driverRegistryEntry{
				number: 44, tla: "AAA", tlaState: driverListFieldValid,
				racingNumberState: driverListFieldValid,
			}),
		},
		{
			name:    "lower number is stored in canonical order",
			payload: `{"7":{"Tla":"BBB"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "BBB", tlaState: driverListFieldValid},
				driverRegistryEntry{number: 44, tla: "AAA", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
			),
		},
		{
			name:    "invalid present acronym cannot become omission",
			payload: `{"44":{"Tla":"\ud800"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "BBB", tlaState: driverListFieldValid},
				driverRegistryEntry{number: 44, tlaState: driverListFieldInvalid, racingNumberState: driverListFieldValid},
			),
			issues: driverListIssueUnicode | driverListIssueIdentity,
		},
		{
			name:    "unrelated field does not restore acronym",
			payload: `{"44":{"RacingNumber":"44"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "BBB", tlaState: driverListFieldValid},
				driverRegistryEntry{number: 44, tlaState: driverListFieldInvalid, racingNumberState: driverListFieldValid},
			),
		},
		{
			name:    "valid replacement restores acronym",
			payload: `{"44":{"Tla":"CCC"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "BBB", tlaState: driverListFieldValid},
				driverRegistryEntry{number: 44, tla: "CCC", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
			),
		},
		{
			name:    "invalid present racing number latches",
			payload: `{"7":{"RacingNumber":"8"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "BBB", tlaState: driverListFieldValid, racingNumberState: driverListFieldInvalid},
				driverRegistryEntry{number: 44, tla: "CCC", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
			),
			issues: driverListIssueIdentity,
		},
		{
			name:    "acronym does not restore racing number",
			payload: `{"7":{"Tla":"DDD"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "DDD", tlaState: driverListFieldValid, racingNumberState: driverListFieldInvalid},
				driverRegistryEntry{number: 44, tla: "CCC", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
			),
		},
		{
			name:    "matching replacement restores racing number",
			payload: `{"7":{"RacingNumber":"7"}}`,
			want: driverRegistryTestState(
				driverRegistryEntry{number: 7, tla: "DDD", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
				driverRegistryEntry{number: 44, tla: "CCC", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
			),
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			got := reduceDriverRegistryRaw(t, state, liveTimingUpdateSourceFeed, step.payload)
			wantDisposition := driverRegistryDispositionStaged
			if got.state == state {
				wantDisposition = driverRegistryDispositionNoUpdate
			}
			want := driverRegistryReduction{state: step.want, disposition: wantDisposition, issues: step.issues}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("reduction =\n%#v\nwant\n%#v", got, want)
			}
			if got.state.frozen || got.state.synchronized {
				t.Fatalf("feed froze or synchronized registry: %#v", got.state)
			}
			state = got.state
		})
	}
}

func TestReduceDriverRegistryFeedScopesInvalidEntries(t *testing.T) {
	got := reduceDriverRegistryRaw(t, driverRegistryState{}, liveTimingUpdateSourceFeed,
		`{"3":{"Tla":"CCC"},"2":{"Tla":"\ud800"},"1":{"Tla":"AAA"}}`)
	want := driverRegistryReduction{
		state: driverRegistryTestState(
			driverRegistryEntry{number: 1, tla: "AAA", tlaState: driverListFieldValid},
			driverRegistryEntry{number: 2, tlaState: driverListFieldInvalid},
			driverRegistryEntry{number: 3, tla: "CCC", tlaState: driverListFieldValid},
		),
		disposition: driverRegistryDispositionStaged,
		issues:      driverListIssueUnicode | driverListIssueIdentity,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reduction =\n%#v\nwant\n%#v", got, want)
	}

	malformedRoot := reduceDriverRegistryRaw(t, got.state, liveTimingUpdateSourceFeed, `[]`)
	want.state.synchronized = false
	want.disposition = driverRegistryDispositionUnavailable
	want.issues = driverListIssueShape
	if !reflect.DeepEqual(malformedRoot, want) {
		t.Fatalf("malformed root =\n%#v\nwant\n%#v", malformedRoot, want)
	}
}

func TestReduceDriverRegistryBoundsStagedFeed(t *testing.T) {
	first := reduceDriverRegistryRaw(t, driverRegistryState{}, liveTimingUpdateSourceFeed, syntheticDriverList(1, 33))
	wantEntries := syntheticRegistryEntries(1, maxDriverRegistryEntries)
	for index := range wantEntries {
		wantEntries[index].racingNumberState = driverListFieldValid
	}
	wantFirst := driverRegistryReduction{
		state:       driverRegistryTestState(wantEntries...),
		disposition: driverRegistryDispositionStaged,
		issues:      driverListIssueLimit,
	}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("33-entry feed reduction =\n%#v\nwant\n%#v", first, wantFirst)
	}

	full := driverRegistryTestState(syntheticRegistryEntries(100, maxDriverRegistryEntries)...)
	overflow := reduceDriverRegistryRaw(t, full, liveTimingUpdateSourceFeed, `{"999":{"Tla":"ZZZ"}}`)
	want := driverRegistryReduction{
		state:       full,
		disposition: driverRegistryDispositionNoUpdate,
		issues:      driverListIssueLimit,
	}
	if !reflect.DeepEqual(overflow, want) {
		t.Fatalf("full-state overflow =\n%#v\nwant\n%#v", overflow, want)
	}
}

func TestReduceDriverRegistrySnapshotKeepsUnknownUnicodeAtIssueScope(t *testing.T) {
	got := reduceDriverRegistryRaw(t, driverRegistryState{}, liveTimingUpdateSourceSnapshot,
		`{"\ud800":{"Tla":"IGNORED"},"7":{"Tla":"ABC","\udfff":"opaque","Future":"\ud800"}}`)
	wantState := driverRegistryTestState(driverRegistryEntry{
		number: 7, tla: "ABC", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid,
	})
	wantState.frozen = true
	wantState.synchronized = true
	want := driverRegistryReduction{
		state: wantState, disposition: driverRegistryDispositionFrozen, issues: driverListIssueUnicode,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown Unicode snapshot =\n%#v\nwant\n%#v", got, want)
	}
}

func TestReduceDriverRegistryFreezesOnlyCoherentSnapshot(t *testing.T) {
	staged := driverRegistryTestState(driverRegistryEntry{number: 99, tla: "OLD", tlaState: driverListFieldValid})
	got := reduceDriverRegistryRaw(t, staged, liveTimingUpdateSourceSnapshot,
		`{"2":{"Tla":"BBB"},"1":{"RacingNumber":"1","Tla":"AAA"},"_kf":true}`)
	wantState := driverRegistryTestState(
		driverRegistryEntry{number: 1, tla: "AAA", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
		driverRegistryEntry{number: 2, tla: "BBB", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
	)
	wantState.frozen = true
	wantState.synchronized = true
	want := driverRegistryReduction{state: wantState, disposition: driverRegistryDispositionFrozen}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("freeze =\n%#v\nwant\n%#v", got, want)
	}

	tests := []struct {
		name    string
		payload string
		issues  driverListIssueSet
	}{
		{name: "empty", payload: `{}`, issues: driverListIssueIdentity},
		{name: "missing acronym", payload: `{"1":{"RacingNumber":"1"}}`, issues: driverListIssueIdentity},
		{name: "malformed acronym", payload: `{"1":{"Tla":"\ud800"}}`, issues: driverListIssueUnicode | driverListIssueIdentity},
		{name: "non-object entry", payload: `{"1":null}`, issues: driverListIssueShape | driverListIssueIdentity},
		{name: "duplicate entry", payload: `{"1":{"Tla":"AAA"},"\u0031":{"Tla":"AAA"}}`, issues: driverListIssueShape | driverListIssueIdentity},
		{name: "too many", payload: syntheticDriverList(1, 33), issues: driverListIssueLimit},
		{name: "non-object root", payload: `[]`, issues: driverListIssueShape},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := reduceDriverRegistryRaw(t, staged, liveTimingUpdateSourceSnapshot, test.payload)
			want := driverRegistryReduction{
				state:       staged,
				disposition: driverRegistryDispositionUnavailable,
				issues:      test.issues,
			}
			if !reflect.DeepEqual(result, want) {
				t.Fatalf("incoherent snapshot =\n%#v\nwant\n%#v", result, want)
			}
		})
	}
}

func TestReduceDriverRegistryFrozenAgreementAndConflict(t *testing.T) {
	frozen := driverRegistryTestState(
		driverRegistryEntry{number: 1, tla: "AAA", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
		driverRegistryEntry{number: 2, tla: "BBB", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
	)
	frozen.frozen = true
	frozen.synchronized = false

	agreeing := reduceDriverRegistryRaw(t, frozen, liveTimingUpdateSourceSnapshot,
		`{"2":{"RacingNumber":"2","Tla":"BBB"},"1":{"Tla":"AAA"}}`)
	wantAgreed := frozen
	wantAgreed.synchronized = true
	if want := (driverRegistryReduction{state: wantAgreed, disposition: driverRegistryDispositionRefreshed}); !reflect.DeepEqual(agreeing, want) {
		t.Fatalf("agreement =\n%#v\nwant\n%#v", agreeing, want)
	}

	tests := []struct {
		name    string
		payload string
		issues  driverListIssueSet
	}{
		{name: "missing frozen driver", payload: `{"1":{"Tla":"AAA"}}`, issues: driverListIssueConflict},
		{name: "new driver", payload: `{"1":{"Tla":"AAA"},"2":{"Tla":"BBB"},"3":{"Tla":"CCC"}}`, issues: driverListIssueConflict},
		{name: "acronym conflict", payload: `{"1":{"Tla":"AAA"},"2":{"Tla":"CCC"}}`, issues: driverListIssueConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reduceDriverRegistryRaw(t, wantAgreed, liveTimingUpdateSourceSnapshot, test.payload)
			wantState := wantAgreed
			wantState.synchronized = false
			want := driverRegistryReduction{state: wantState, disposition: driverRegistryDispositionConflict, issues: test.issues}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("conflict =\n%#v\nwant\n%#v", got, want)
			}
		})
	}

	malformed := reduceDriverRegistryRaw(t, wantAgreed, liveTimingUpdateSourceSnapshot,
		`{"1":{"Tla":"AAA"},"2":{"RacingNumber":"3","Tla":"BBB"}}`)
	wantUnavailable := wantAgreed
	wantUnavailable.synchronized = false
	if want := (driverRegistryReduction{
		state: wantUnavailable, disposition: driverRegistryDispositionUnavailable, issues: driverListIssueIdentity,
	}); !reflect.DeepEqual(malformed, want) {
		t.Fatalf("malformed frozen snapshot =\n%#v\nwant\n%#v", malformed, want)
	}
}

func TestReduceDriverRegistryFrozenFeedCannotRewriteOrResynchronize(t *testing.T) {
	frozen := driverRegistryTestState(
		driverRegistryEntry{number: 1, tla: "AAA", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
		driverRegistryEntry{number: 2, tla: "BBB", tlaState: driverListFieldValid, racingNumberState: driverListFieldValid},
	)
	frozen.frozen = true
	frozen.synchronized = true

	tests := []struct {
		name    string
		payload string
		issues  driverListIssueSet
	}{
		{name: "new roster member", payload: `{"3":{"Tla":"CCC"}}`, issues: driverListIssueConflict},
		{name: "acronym conflict", payload: `{"2":{"Tla":"CCC"}}`, issues: driverListIssueConflict},
		{name: "racing-number conflict", payload: `{"2":{"RacingNumber":"3"}}`, issues: driverListIssueIdentity | driverListIssueConflict},
		{name: "malformed entry", payload: `{"2":null}`, issues: driverListIssueShape | driverListIssueIdentity | driverListIssueConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reduceDriverRegistryRaw(t, frozen, liveTimingUpdateSourceFeed, test.payload)
			wantState := frozen
			wantState.synchronized = false
			want := driverRegistryReduction{state: wantState, disposition: driverRegistryDispositionConflict, issues: test.issues}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("feed conflict =\n%#v\nwant\n%#v", got, want)
			}
		})
	}

	matching := reduceDriverRegistryRaw(t, frozen, liveTimingUpdateSourceFeed,
		`{"2":{"RacingNumber":"2","Tla":"BBB","Future":"\ud800"}}`)
	if want := (driverRegistryReduction{state: frozen}); !reflect.DeepEqual(matching, want) {
		t.Fatalf("matching feed =\n%#v\nwant\n%#v", matching, want)
	}

	unsynchronized := frozen
	unsynchronized.synchronized = false
	stillUnsynchronized := reduceDriverRegistryRaw(t, unsynchronized, liveTimingUpdateSourceFeed,
		`{"1":{"Tla":"AAA"},"2":{"Tla":"BBB"}}`)
	if want := (driverRegistryReduction{state: unsynchronized}); !reflect.DeepEqual(stillUnsynchronized, want) {
		t.Fatalf("feed resynchronization =\n%#v\nwant\n%#v", stillUnsynchronized, want)
	}
}

func TestReduceDriverRegistryRejectsUnknownSourceWithoutStateLoss(t *testing.T) {
	state := driverRegistryTestState(driverRegistryEntry{number: 7, tla: "ABC", tlaState: driverListFieldValid})
	parsed, err := parseDriverList(json.RawMessage(`{"8":{"Tla":"DEF"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reduceDriverRegistry(state, 0, parsed)
	if !errors.Is(err, errInvalidDriverRegistryUpdate) || got != (driverRegistryReduction{}) {
		t.Fatalf("unknown source result = %#v, %v", got, err)
	}
}

func TestDriverRegistryResultShapesStayBounded(t *testing.T) {
	for _, test := range []struct {
		value  any
		fields string
	}{
		{driverListEntryPatch{}, "number object duplicate tla tlaState racingNumberState"},
		{driverListParseResult{}, "entries count object issues"},
		{driverRegistryEntry{}, "number tla tlaState racingNumberState"},
		{driverRegistryState{}, "drivers count frozen synchronized"},
		{driverRegistryReduction{}, "state disposition issues"},
	} {
		typeOf := reflect.TypeOf(test.value)
		var names []string
		for index := 0; index < typeOf.NumField(); index++ {
			names = append(names, typeOf.Field(index).Name)
		}
		if got := strings.Join(names, " "); got != test.fields {
			t.Fatalf("review full oracle for %s: %s, want %s", typeOf, got, test.fields)
		}
	}
}

func reduceDriverRegistryRaw(
	t *testing.T,
	state driverRegistryState,
	source liveTimingUpdateSource,
	payload string,
) driverRegistryReduction {
	t.Helper()
	parsed, err := parseDriverList(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	got, err := reduceDriverRegistry(state, source, parsed)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func driverRegistryTestState(entries ...driverRegistryEntry) driverRegistryState {
	var state driverRegistryState
	copy(state.drivers[:], entries)
	state.count = uint8(len(entries))
	return state
}

func syntheticRegistryEntries(start, count int) []driverRegistryEntry {
	entries := make([]driverRegistryEntry, count)
	for index := range entries {
		entries[index] = driverRegistryEntry{
			number: int64(start + index), tla: syntheticDriverTLA(index),
			tlaState: driverListFieldValid,
		}
	}
	return entries
}

func syntheticDriverList(start, count int) string {
	var result strings.Builder
	result.WriteByte('{')
	for index := 0; index < count; index++ {
		if index != 0 {
			result.WriteByte(',')
		}
		number := start + index
		fmt.Fprintf(&result, "%q:{\"RacingNumber\":%q,\"Tla\":%q}",
			fmt.Sprint(number), fmt.Sprint(number), syntheticDriverTLA(index))
	}
	result.WriteByte('}')
	return result.String()
}

func syntheticDriverTLA(index int) string {
	return string([]byte{
		'A' + byte(index/(26*26)%26),
		'A' + byte(index/26%26),
		'A' + byte(index%26),
	})
}
