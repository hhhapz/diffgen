# diffgen

A tool to code generate the differences between two Go structs using compile-time generated code.

```
Usage of diffgen:
	diffgen [flags] -type T [directory]
Flags:
  -output string
    	output file name; default srcdir/<type>_diffgen.go
  -skip
    	skip unhandled or unknown types instead of failing
  -type string
    	the source type to generate the diff from
```