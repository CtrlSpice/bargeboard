# Reviewed source attribution and supplements

`../attribution.go` associates complete source comment groups and supplemental
texts with the pinned owning module, its Go checksum, and selected source path.
These are the approved attribution-cleanup inputs for the five-target release
graph at `b87dc4b02a1ceaddc1cfaa1a91c689c4cc5ba042`. They approve no license family
or permission fragment generally. The existing MPL source archive requirements
remain separate; none of these additions requires a binary source offer.

Go comments are selected throughout each parsed file, in lexical order. Whole
groups retain their comment delimiters, indentation, technical discussion, and
legal text. CRLF becomes LF and a missing final LF is supplied. Labels retain the
existing `source-header:<path>#<index>` spelling for output continuity, including
groups that occur after the package declaration. Non-Go collection retains its
leading-comment scope. The selector additionally recognizes `MIT license`,
`BSD license`, and `released under`; it does not select every use of `license`.

## Source approvals

The complete normalized group SHA-256 values and exact permitted source paths are
listed in `reviewedAttributions`. A changed owning version/checksum, missing
selected source, or changed/missing required group fails closed. Every other
selected group still passes source-license classification, including separate MPL
or GPL notices. Existing legal-document hash approvals do not classify source
notices. Supplemental files have their own complete normalized text pins.

| Material | Reviewed scope and terms |
|---|---|
| Sun/fdlibm | Fifteen Go 1.26.8 math groups; retain the notice-preservation grant and all three attribution variants: 1993 SunPro, 1993 SunSoft (`cbrt.go`), and 2004 Sun (`exp.go`). |
| Cephes | Six distinct custom free-use/no-support-or-guarantee groups across twelve Go files and Gonum `internal/cmplx64/sqrt.go`. Preserve the descriptive commercial-product passage. Gonum's supplemental BSD document applies to Gonum; it does not relicense Go's copies. |
| fiat-crypto | Go `src/crypto/internal/fips140/edwards25519/scalar.go:29–55`; exact BSD-1-Clause group, including the original Berkeley Software Design disclaimer. Its retention condition applies to source redistribution, without requiring a source offer with binaries. |
| grpc-gateway | `runtime/pattern.go:266–273`, Go Authors 2009 BSD reference, plus the already-collected `internal/casing/LICENSE.md` Go BSD terms. Both the complete source reference and supporting document are checked. |
| ntgo, half-rs, LINPACK | The exact references and supporting materials described below. |

The Go fixture source is the complete Go 1.26.8 darwin-arm64 SDK:
<https://go.dev/dl/go1.26.8.darwin-arm64.tar.gz>, archive SHA-256
`a012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b`.
The source groups retain their original FreeBSD/Netlib references. Those historical
URLs identify provenance; the pinned Go archive supplies the reviewed bytes.
The fiat-crypto notice is corroborated by
[LICENSE-BSD-1 at v0.0.9](https://github.com/mit-plv/fiat-crypto/blob/23d2dbc4ab897d14bde4404f70cd6991635f9c01/LICENSE-BSD-1),
SHA-256 `0c1240e29b4a2c528bcc3ccc38a98b1dba5e3e6b465da910208935f082e2ced6`.

## Owning modules and source witnesses

| Owning module | Go module checksum | Selected source and raw SHA-256 |
|---|---|---|
| `github.com/grpc-ecosystem/grpc-gateway/v2@v2.29.0` | `h1:5VipnvEpbqr2gA2VbM+nYVbkIF28c5ZQfqCBQ5g2xfk=` | `runtime/pattern.go`: `babf890951c6cf8bcfdfc1e74b7797bad0de3fdbfd4c3ecb3410934e86252ac2` |
| `github.com/shirou/gopsutil/v4@v4.26.7` | `h1:IXzpHz/dkMRYAhKkOXr1HB6SuzWU3eoyyeWe7g3bNZc=` | `internal/common/endian.go`: `cd16be328bef13a20d71dc380f0551b8e2406a84354750cfc77cebc65e4fd424` |
| `github.com/x448/float16@v0.8.4` | `h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM=` | `float16.go`: `9218ebb08f57932871049a3aa35a9da8e56f0117e56ff44d36713d562b81505b` |
| `gonum.org/v1/gonum@v0.17.0` | `h1:VbpOemQlsSMrYmn7T2OUvQ4dqxQXU+ouZFQsZOx50z4=` | `internal/cmplx64/sqrt.go`: `b710cee134b376cfbdc53437c3ab7da181682888ef1988e95ef2d239515b61d5`; `mat/cholesky.go`: `a397b3bb624daf26fe4f653cda9faf0a74a6530a535cced9b7e525dcad88c1c9` |

The grpc-gateway witnesses are
[runtime/pattern.go](https://github.com/grpc-ecosystem/grpc-gateway/blob/v2.29.0/runtime/pattern.go)
and [internal/casing/LICENSE.md](https://github.com/grpc-ecosystem/grpc-gateway/blob/v2.29.0/internal/casing/LICENSE.md)
from its checksum-pinned v2.29.0 module.

Module version/checksum and source-group matching are the executable scope checks.
Raw source hashes above document the reviewed witnesses, rather than imposing an
additional whole-source-file transformation policy.

### ntgo

[gopsutil endian.go](https://github.com/shirou/gopsutil/blob/52a24c8dea6bf6ed951209ed280d1b827d1e3ac8/internal/common/endian.go)
explicitly copies `IsLittleEndian` from ntgo v0.8.0. Retain both ntgo notices:

- `ntgo-LICENSE.txt`: [root LICENSE](https://github.com/ntrrg/ntgo/blob/8c248e7182677bccef8f7b471a0180bb1db5618b/LICENSE),
  Copyright (c) 2018 Miguel Angel Rivera Notararigo; raw and normalized SHA-256
  `fa92e73eadb684fd08a7b6d7cfa995129acbb42b0f286a134f0f4c607a9db002`.
- `ntgo-source-notice.txt`: exact [runtime/infrastructure.go lines 1–2](https://github.com/ntrrg/ntgo/blob/8c248e7182677bccef8f7b471a0180bb1db5618b/runtime/infrastructure.go#L1-L2),
  Copyright 2021 Miguel Angel Rivera Notararigo; raw and normalized slice SHA-256
  `8edb93ed37205083efeb76cb2fcb1a43c46d17ed8b180ca6693d9d32296306e4`.
  Complete upstream source SHA-256:
  `defc9139cb34f372fefa736bf534614059bbb050c06d62029364392102bff038`.

### half-rs

[float16.go at v0.8.4](https://github.com/x448/float16/blob/cb9afec31f2649663ebb64da5c6c32c3d365c3ca/float16.go)
credits Kathryn Long in its leading header and conversion-body reference.
`half-rs-LICENSE-MIT.txt` retains her 2016 copyright and full MIT terms from
[half-rs v1.4.0](https://github.com/starkat99/half-rs/blob/5f01541c72030584f2160816b9b4c7ee45a4b657/LICENSE-MIT).
Raw SHA-256 is
`54c0366c7f8643c8502618e7f02b4176f28c834a6b0315b969477720b65e183f`;
normalized SHA-256 is
`02f1e4709f1c03c99e2cb4de81f694cc41fb26acc14c0e6f3ee74a4852f0e4e8`.

The retained `half-rs-provenance.txt`, SHA-256
`829496a3477f967675ddf5e12a39df91d3a9a5723a10e0b8650530c68331e310`,
records the bounded source witness: float16 names no exact half-rs import
revision. v1.4.0's matching conversion code is contemporary evidence, not a proven
import pin. The MIT bytes also match v1.0.0 at
`dba6126dd4a7cdb637dc0d8882d30e3818e1d002`. This provenance supplement is explanatory
project material; it adds no conditions to the upstream MIT text.

### Gonum's explicit supplemental filenames

Gonum v0.17.0 is commit `fc402bc485e3a92f8d4f1f0ee5a49e2edf232ed2`.
Its pinned module ZIP SHA-256 is
`dadeb3d260548de27375f7188bdfd5a175110120fd8ee8085a63b0dff2cae501`.
Two files are read directly from this module with exact normalized text pins:

- [THIRD_PARTY_LICENSES/Cephes-LICENSE](https://github.com/gonum/gonum/blob/fc402bc485e3a92f8d4f1f0ee5a49e2edf232ed2/THIRD_PARTY_LICENSES/Cephes-LICENSE):
  `aec748e1a0450cad63a27d19af58754df0bf20b90bc4472a873e88eb29b53afb`.
  Associated with the selected Cephes-derived `internal/cmplx64/sqrt.go`, alongside
  its complete custom source notice.
- [THIRD_PARTY_LICENSES/Go-LICENSE](https://github.com/gonum/gonum/blob/fc402bc485e3a92f8d4f1f0ee5a49e2edf232ed2/THIRD_PARTY_LICENSES/Go-LICENSE):
  `2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067`.
  Associated with the same selected Go-derived source; Go attributions also occur
  in selected `internal/math32`, `internal/asm/f64`, and `graph/iterator` sources.
  The text already exists under Kubernetes labels, but its Gonum association was
  missing. Raw and normalized hashes are identical for both Gonum documents.

The directory contains ten other documents. This is a module-specific supplement
selection, not a generic directory-discovery or license-exclusion rule:

| Other filename(s) | Recorded source scope outside the selected graph |
|---|---|
| `Bogaert-LICENSE`, `Oxford-LICENSE` | `integrate/quad` Legendre/Hermite code |
| `Boost-LICENSE`, `Probab-LICENSE` | `mathext` Lanczos/erf code |
| `MT19937-LICENSE`, `MT19937-64-LICENSE` | `mathext/prng` |
| `Fike-LICENSE` | `num/dual`, `num/dualquat`, `num/hyperdual` |
| `Sun-LICENSE` | `internal/math32/math_test.go`, an unselected test |
| `W3C-BSD-LICENSE`, `W3C-TestSuite-LICENSE` | `graph/formats/rdf` test data |

The existing module-wide legal walk still collects the already-approved RDF
test-data *reference* in `graph/formats/rdf/testdata/LICENSE.md`. That approval does
not approve the two full W3C documents. Broader discovery would need separate
review, including Boost, W3C's modification restrictions, and Bogaert's document
without an explicit grant. The selected-package policy does not depend on linker
dead-code elimination.

### LINPACK

`LINPACK.txt` is the approved supplement, exact SHA-256
`96b2548659f6bc12082f523a99c81a143cf3377265215b9d701aed36b983d860`.
It retains Stewart's attribution, the LINPACK development credit, and the recorded
Dongarra statement that LINPACK is licensed under Modified 3-clause BSD.
[Fedora comment 13](https://bugzilla.redhat.com/show_bug.cgi?id=1000829#c13)
contains the quotation; [comment 25](https://bugzilla.redhat.com/show_bug.cgi?id=1000829#c25)
reports upstream confirmation of the complete text. Their decoded comment-text
SHA-256 values are, respectively,
`20c5a03f90119037f6925ee7161ebfd16b9001150d5f57df2e802e83d6115c7d` and
`ef5e4ee93661a4d03d6075b9693627d5e037c3c901c2e174984a4d8354389cfe`.

The standard BSD-3 conditions/disclaimer come from
[SPDX license-list-data v3.27.0](https://github.com/spdx/license-list-data/blob/d46e94e2c78ceede1cfc63cfa0396472d2798d4c/text/BSD-3-Clause.txt),
raw SHA-256 `5a93d5831e1297ab10fe643e1a631e83be392896da14ee2951285a79012df69d`.
The supplement explicitly identifies this as a standard-text reproduction, not a
recovered LINPACK license file or complete email; it invents no copyright owner
or year. The routine version date remains a version date.

| Source witness | Raw SHA-256 |
|---|---|
| [Netlib dchud.f](https://www.netlib.org/linpack/dchud.f) | `b48645e93a403c2d8ac366f860b346d6980234d9102031f311b84a82d3263913` |
| [Netlib dchdd.f](https://www.netlib.org/linpack/dchdd.f) | `23c53288a520d68e0e1f6381be20d05d1a45fc60567af2c8446e2fe96b419a49` |
| [Netlib readme](https://www.netlib.org/linpack/readme) | `d857e7683bf6d55431b9cf8ca66bb82d028d96d256f89c6f1081f61150693054` |

The routine bytes also match `ivan-pi/LINPACK` commit
`cb7942cf843e59c78fc3b48b45ff4b40b2c048af` (`src/dchud.f`, `src/dchdd.f`).
That comparison establishes routine identity; the recorded author confirmation
establishes the accepted license family. All supplemental text is checked in or
read from the pinned owning module, with no fresh network requirement.

## Fixtures and verification

`../testdata` holds full normalized groups, without editing the legal or technical
passages: `cbrt.txt` (Go `src/math/cbrt.go:7–17`), `cephes.txt` (Go
`src/math/cmplx/sqrt.go:9–29`, also Gonum `internal/cmplx64/sqrt.go:13–33`),
`fiat.txt` (Go scalar source above), `grpc.txt` (`runtime/pattern.go:266–273`),
`ntgo.txt` (`internal/common/endian.go:6–7`), `half-rs-header.txt`
(`float16.go:1–4`), `half-rs.txt` (`float16.go:256–258`), and `LINPACK.txt`
(`mat/cholesky.go:587–611`). Fixture hashes are asserted against the reviewed
groups. The two `*-LICENSE.txt` fixtures reproduce the Gonum documents;
`../grpc_license_test.go` retains grpc-gateway's complete
`internal/casing/LICENSE.md`, including its final blank line. Its normalized SHA-256 is
`20873bd48d04a4e4075ee881ec2a5f3a3b14bf4920018b1131a7c52ec9b4a318`.

Focused tests verify full-byte selection in synthetic Go syntax, literal-string
exclusion, malformed input, normalization, module/source scope, exact supplemental
associations, changed/additional terms, MPL source enforcement, material bounds,
and a complete deterministic render oracle. Required repository checks are
`make check`, `go test -race -count=1 ./scripts/thirdparty`, and
`git diff --check`.

## Complete output delta

With the complete Go 1.26.8 SDK, the five-target graph contains 178 components.
The cleanup adds 42 material associations: the 32 post-package groups from the
original inventory, the ntgo/half-rs/LINPACK references missed by the selector,
and seven supplemental associations. These contribute 34 new distinct texts;
Gonum's Go BSD document already existed under other module labels. The existing
grpc-gateway Go BSD document is verified in place and adds no duplicate label.
[attribution-delta.tsv](attribution-delta.tsv) records every addition as
tab-separated owning component, material label, and normalized SHA-256.

| Complete representation | Before | After |
|---|---|---|
| Notices bytes | 1,296,152 | 1,369,521 |
| Notices SHA-256 | `4be6ba71b1153e0937f54f465c8a7110bdf56911e300ce4a20df5041498fb1a0` | `40ecd0bf944708aa75f9ea1b7db1d7d5e9dc4c296ebe9d214d97af489923011b` |
| Full inventory SHA-256 | `11e5cfe4caa46fe1920b33d0f244e5b9e1f08e0b7e5b2dd6786204937d3c99c8` | `c48a3a569cb472c6a82a2165ab8847a5e9a1ae0a09d3d1a2c52881aebd3d659b` |

The inventory proof sorts LF-terminated TSV records lexically: `Module`, component
key, module sum; `Legal`, component key, label, normalized SHA-256, byte length;
and `Source`, component key, archive name, archive SHA-256. It includes every
material association, including unchanged ones. Restoring the old Go leading-only
selector and removing exactly the supplemental additions reproduces the old
whole-output pin byte-for-byte. Module records and all three source archives are
unchanged. The comparison also checks all 32 original source occurrences against
their complete source-file and comment-group evidence and exercises all 34 scoped
source approvals (including the existing half-rs header) against mutations and
additional terms. No unchecked production override is provided.
