package circuitmap

import (
	"fmt"
	"strings"
	"testing"
)

const point = `{"number":1,"trackPosition":{"x":-1000000,"y":1e6}}`
const minimal = `{"circuitKey":91,"year":2026,"corners":[` + point + `]}`

func TestParseValidation(t *testing.T) {
	check := func(raw string, valid bool) {
		t.Helper()
		m, err := Parse([]byte(raw), 91, 2026)
		if (err == nil) != valid || m.Valid() != valid {
			t.Errorf("size %d, prefix %.120s: valid %t, err %v", len(raw), raw, m.Valid(), err)
		}
	}
	for _, raw := range []string{minimal, strings.Repeat(" ", maxBodyBytes-len(minimal)) + minimal,
		strings.Replace(minimal, `"number":`, `"letter":"","unknown":0,"unknown":1,"number":`, 1),
	} {
		check(raw, true)
	}
	for _, tc := range [][2]string{
		{`91`, `92`}, {`91`, `91.0`}, {`91`, `"91"`}, {`91`, `0`}, {`2026`, `2.026e3`}, {`2026`, `2025`},
		{`"year":`, `"year":2026,"\u0079ear":`}, {`"number":`, `"letter":null,"number":`},
		{`"year":`, `"rotation":1e400,"year":`}, {`"year":`, `"rotation":null,"year":`},
		{`"year":`, `"marshalSectors":null,"year":`}, {`"year":`, `"marshalSectors":[` + point + `,` + point + `],"year":`},
		{point, ``}, {point, point + `,` + point}, {point, `null`}, {`"number":1`, `"number":0`}, {`"number":1`, `"number":65`},
		{`"number":1`, `"number":1e0`}, {`"number":1`, `"number":1,"number":2`},
		{`"number":1`, `"number":1,"letter":"a"`}, {`"number":1`, `"number":1,"letter":"AB"`}, {`"number":1`, `"number":1,"letter":"É"`},
		{`"x":`, `"x":3,"\u0078":`}, {`"y":`, `"z":`}, {`"trackPosition":`, `"else":`},
		{`-1000000`, `-1000001`}, {`1e6`, `1000001`}, {`1e6`, `1e400`}, {`1e6`, `null`}, {`1e6`, `true`}, {`1e6`, `"0"`},
		{`1e6`, `NaN`}, {`1e6`, `Inf`}, {`1e6`, `1000000.0000000000001`},
	} {
		check(strings.Replace(minimal, tc[0], tc[1], 1), false)
	}
	check(minimal+`{}`, false)
	check(strings.Repeat(" ", maxBodyBytes-len(minimal)+1)+minimal, false)
	var points []string
	for i := 1; i <= 64; i++ {
		points = append(points, strings.Replace(point, `"number":1`, fmt.Sprintf(`"number":%d`, i), 1))
	}
	full := strings.Join(points, ",")
	check(strings.Replace(minimal, point, full, 1), true)
	check(strings.Replace(minimal, point, full+`,`+strings.Replace(point, `"number":1`, `"number":1,"letter":"A"`, 1), 1), false)
	for _, v := range []struct {
		extra string
		valid bool
	}{{full, true}, {full + `,` + point, false}, {``, true}} {
		check(strings.Replace(minimal, `"year":`, `"marshalSectors":[`+v.extra+`],"year":`, 1), v.valid)
	}
}

func TestUnicodeIssues(t *testing.T) {
	for _, tc := range []struct {
		field, name string
		issues      bool
	}{
		{`"circuitName":"\ud800"`, "", true},
		{`"circuitName":"Test","unused":"\udfff"`, "Test", true},
		{`"circuitName":"Test"`, "Test", false},
	} {
		m, err := Parse([]byte(strings.Replace(minimal, `"year":`, tc.field+`,"year":`, 1)), 91, 2026)
		if err != nil || !m.Valid() || m.name != tc.name || m.HasUnicodeIssues() != tc.issues {
			t.Errorf("%s: name %q, issues %t, err %v", tc.field, m.name, m.HasUnicodeIssues(), err)
		}
	}
}
