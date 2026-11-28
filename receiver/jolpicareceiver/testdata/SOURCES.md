# Jolpica test data

The [Jolpica Ergast API documentation](https://github.com/jolpica/jolpica-f1/tree/main/docs)
describes the `MRData` pagination fields, Race and Sprint result tables, and
driver and constructor standings. The JSON files here use those field names
with invented competitors, results and points. None is a captured response.

The HTTP tests simulate delayed publication, rate limits and redirects on
loopback. They do not measure publication timing or contact Jolpica.
