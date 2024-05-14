package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/tools/go/packages"
)

var hasMap bool

var (
	typeName = flag.String("type", "", "the source type to generate the diff from")
	skip     = flag.Bool("skip", false, "skip unhandled or unknown types instead of failing")
	output   = flag.String("output", "", "output file name; default srcdir/<type>_diffgen.go")
	methods  = flag.Bool("methods", false, "include methods in diff")
)

func Usage() {
	fmt.Fprintf(os.Stderr, "Usage of diffgen:\n")
	fmt.Fprintf(os.Stderr, "\tdiffgen [flags] -type T [directory]\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("diffgen: ")
	flag.Usage = Usage
	flag.Parse()
	if len(*typeName) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	args := flag.Args()
	if len(args) == 0 {
		// process whole package
		args = []string{"."}
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedCompiledGoFiles | packages.NeedName | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
	}, args...)
	if err != nil {
		log.Fatal(err)
	}
	if len(pkgs) != 1 {
		log.Fatalf("error: %d packages found", len(pkgs))
	}
	d := DiffGen{
		Package: pkgs[0],
	}
	d.ParseBase()
	if d.base == nil {
		log.Fatalf("expected to find type %s, found none", *typeName)
	}
	fields := ProcessStruct(nil, d.base)
	c := Comparisons{
		Structs: make(map[string]Comparisons),
	}
	for _, path := range fields {
		c.Add(path)
	}
	imp := `import "strconv"`
	if hasMap {
		imp = `import (
	"fmt"
	"slices"
	"strconv"
)`
	}
	out := new(bytes.Buffer)
	fmt.Fprintf(out, "// Code generated by \"diffgen %s\"; DO NOT EDIT.\n", strings.Join(os.Args[1:], " "))
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "package %s\n", d.Name)
	fmt.Fprintf(out, `
%s

type Diff struct {
	Path []string
	A    any
	B    any
}

func mkDiff(path []string, a, b any) Diff {
	return Diff{slices.Clone(path), a, b}
}

func Compare%s(a, b %s) (diff []Diff) {
`, imp, *typeName, *typeName)
	c.WriteComparisons(out, "\t", false)
	fmt.Fprint(out, "\treturn diff\n}\n")
	f := os.Stdout
	if *output == "" {
		*output = strings.ToLower(*typeName) + "_diffgen.go"
	}
	if *output != "" && *output != "-" {
		var err error
		f, err = os.Create(*output)
		if err != nil {
			log.Fatalf("could not create file %s: %v", *output, err)
		}
	}
	io.Copy(f, out)
}

type DiffGen struct {
	*packages.Package

	base   *types.Struct
	prefix string
}

func (d *DiffGen) ParseBase() {
	for i := range d.CompiledGoFiles {
		file := d.Syntax[i]
		for _, decl := range file.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				typ, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if typ.Name.Name != *typeName {
					continue
				}
				t, ok := d.TypesInfo.Types[typ.Type].Type.(*types.Struct)
				if !ok {
					log.Fatalf("expected struct type, instead got %T", t)
				}
				d.base = t
				return
			}
		}
	}
}

func ProcessStruct(path []string, s *types.Struct) [][]string {
	var comparisons [][]string
	for i := 0; i < s.NumFields(); i++ {
		f := s.Field(i)
		if !f.Exported() {
			continue
		}
		nPath := make([]string, 0, len(path)+1)
		nPath = append(nPath, path...)
		res := ProcessType(append(nPath, f.Name()), f.Type())
		comparisons = append(comparisons, res...)
	}
	return comparisons
}

func ProcessType(prefix []string, t types.Type) [][]string {
	switch t := t.(type) {
	case *types.Pointer:
		elem := t.Elem()
		items := ProcessType(append(prefix, "[pointer]"), elem)
		// methods
		methods := types.NewMethodSet(t)
		for i := 0; i < methods.Len(); i++ {
			m := methods.At(i)
			if !unicode.IsUpper(rune(m.Obj().Name()[0])) {
				continue
			}
			prefix := slices.Clone(prefix)
			items = append(items, append(prefix, "[method]", m.Obj().Name()))
		}
		return items
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() != nil {
			switch {
			case obj.Pkg().Name() == "time" && obj.Name() == "Time":
				return [][]string{prefix}
			}
		}
		return ProcessType(prefix, obj.Type().Underlying())
	case *types.Struct:
		return ProcessStruct(prefix, t)
	case *types.Slice:
		elem := t.Elem()
		return ProcessType(append(prefix, "[slice]"), elem)
	case *types.Map:
		hasMap = true
		_, ok := t.Key().Underlying().(*types.Basic)
		if !ok {
			log.Fatal("only basic types for slice supported at the moment")
		}
		return ProcessType(append(prefix, "[map]"), t.Elem())
	case *types.Array, *types.Basic:
		return [][]string{prefix}
	case *types.Interface, *types.Signature, *types.Chan:
		return nil
	default:
		if *skip {
			log.Printf("%s (Skipping: %T)", prefix, t)
			return nil
		}
		log.Fatalf("%s: unknown type %T to handle", prefix, t)
		return nil
	}
}

type Comparisons struct {
	path    []string
	Fields  []string
	Methods []string
	Structs map[string]Comparisons
}

func (c *Comparisons) Add(path []string) {
	if len(path) == 1 {
		c.Fields = append(c.Fields, path[0])
		return
	}
	if len(path) == 2 && path[0] == "[method]" {
		c.Methods = append(c.Methods, path[1])
		return
	}
	if c.Structs == nil {
		c.Structs = make(map[string]Comparisons)
	}
	subC, ok := c.Structs[path[0]]
	if !ok {
		subC.path = make([]string, 0, len(c.path)+1)
		if path[0] != "[map]" && path[0] != "[slice]" {
			subC.path = append(subC.path, c.path...)
			subC.path = append(subC.path, path[0])
		}
		c.Fields = append(c.Fields, path[0])
	}
	subC.Add(path[1:])
	c.Structs[path[0]] = subC
}

func (c *Comparisons) WriteComparisons(w io.Writer, prefix string, usePrefix bool) {
	for _, f := range c.Fields {
		s, ok := c.Structs[f]
		pathA := c.MakePath("a.", (c.path))
		pathB := c.MakePath("b.", (c.path))
		var hasIf bool
		switch {
		case f == "[pointer]":
			p := make([]string, 0, len(c.path)+1)
			for _, item := range c.path {
				if item[0] != '[' {
					p = append(p, "\""+item+"\"")
				}
			}
			diffPath := "[]string{" + strings.Join(p, ", ") + "}"
			if usePrefix {
				diffPath = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
				if len(p) == 0 {
					diffPath = "prefix"
				}
			}
			hasIf = true
			fmt.Fprintf(w, `%[1]sif %[2]s == nil && %[3]s != nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s == nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s != nil {`+"\n",
				prefix, pathA, pathB, diffPath)
			prefix = prefix + "\t"
		case f == "[map]":
			p := make([]string, 0, len(c.path)+1)
			for _, item := range c.path {
				if item[0] != '[' {
					p = append(p, "\""+item+"\"")
				}
			}
			k := `fmt.Sprint(k)`
			diffPath := "[]string{" + strings.Join(p, ", ") + "}"
			var diffPathKey string
			if len(p) == 0 {
				diffPathKey = "[]string{" + k + "}"
			} else {
				diffPathKey = diffPath[:len(diffPath)-1] + ", " + k + "}"
			}
			if usePrefix {
				diffPath = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
				p = append(p, k)
				diffPathKey = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
			}
			if !ok {
				fmt.Fprintf(w, `%[1]sif %[2]s == nil && %[3]s != nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s == nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s != nil {
	%[1]sfor k, va := range %[2]s {
		%[1]svb, ok := %[3]s[k]
		%[1]sif !ok || ok && va != vb {
			%[1]sdiff = append(diff, mkDiff(%[5]s, %[2]s[k], %[3]s[k]))
		%[1]s}
	%[1]s}
	%[1]sfor k := range %[3]s {
		%[1]s_, ok := %[2]s[k]
		%[1]sif !ok { // Only append it if it's not in the original map, since if it is inside, it's already checked.
			%[1]sdiff = append(diff, mkDiff(%[5]s, %[2]s[k], %[3]s[k]))
		%[1]s}
	%[1]s}
%[1]s}
`,
					prefix, pathA, pathB, diffPath, diffPathKey)
			} else {
				fmt.Fprintf(w, `%[1]sif %[2]s == nil && %[3]s != nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s == nil {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s} else if %[2]s != nil && %[3]s != nil {
	%[1]sfor k, va := range %[2]s {
		%[1]svb, ok := %[3]s[k]
		%[1]sif !ok {
			%[1]sdiff = append(diff, mkDiff(%[5]s, %[2]s[k], %[3]s[k]))
		%[1]s} else {
			%[1]sa := va
			%[1]sb := vb
			%[1]sprefix := %[5]s
`,
					prefix, pathA, pathB, diffPath, diffPathKey)
				prefix = prefix + "\t\t\t"
				s.WriteComparisons(w, prefix, true)
				prefix = prefix[:len(prefix)-3]
				fmt.Fprintf(w, `		%[1]s}
	%[1]s}
	%[1]sfor k := range %[3]s {
		%[1]s_, ok := %[2]s[k]
		%[1]sif !ok { // Only append it if it's not in the original map, since if it is inside, it's already checked.
			%[1]sdiff = append(diff, mkDiff(%[5]s, %[2]s[k], %[3]s[k]))
		%[1]s}
	%[1]s}
%[1]s}
`,
					prefix, pathA, pathB, diffPath, diffPathKey)
				continue
			}
			continue
		case f == "[slice]":
			p := make([]string, 0, len(c.path)+1)
			for _, item := range c.path {
				if item[0] != '[' {
					p = append(p, "\""+item+"\"")
				}
			}
			diffPath := "[]string{" + strings.Join(p, ", ") + "}"
			var diffPathIdx string
			if len(p) == 0 {
				diffPathIdx = "[]string{strconv.Itoa(i)}"
			} else {
				diffPathIdx = diffPath[:len(diffPath)-1] + ", strconv.Itoa(i)}"
			}
			if usePrefix {
				diffPath = "prefix"
				if len(p) > 0 {
					diffPath = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
				}
				p = append(p, "strconv.Itoa(i)")
				diffPathIdx = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
			}
			if !ok {
				fmt.Fprintf(w, `%[1]sif len(%[2]s) == len(%[3]s) {
	%[1]sfor i := range %[2]s {
		%[1]sif %[2]s[i] != %[3]s[i] {
			%[1]sdiff = append(diff, mkDiff(%[5]s, %[2]s[i], %[3]s[i]))
		%[1]s}
	%[1]s}
%[1]s} else {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s}
`,
					prefix, pathA, pathB, diffPath, diffPathIdx)
			} else {
				fmt.Fprintf(w, `%[1]sif len(%[2]s) == len(%[3]s) {
	%[1]sfor i := range %[2]s {
		%[1]sa := %[2]s[i]
		%[1]sb := %[3]s[i]
		%[1]sprefix := %[4]s
`,
					prefix, pathA, pathB, diffPathIdx)
				prefix = prefix + "\t\t"
				s.WriteComparisons(w, prefix, true)
				prefix = prefix[:len(prefix)-2]
				fmt.Fprintf(w, `	%[1]s}
%[1]s} else {
	%[1]sdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))
%[1]s}
`,
					prefix, pathA, pathB, diffPath)
			}
			continue
		case !ok:
			pathA += "." + f
			pathB += "." + f

			p := make([]string, 0, len(c.path)+1)
			for _, item := range c.path {
				if item[0] != '[' {
					p = append(p, "\""+item+"\"")
				}
			}
			p = append(p, "\""+f+"\"")
			diffPath := "[]string{" + strings.Join(p, ", ") + "}"
			if usePrefix {
				diffPath = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
			}
			fmt.Fprintf(w, "%[1]sif %[2]s != %[3]s {\n%[1]s\tdiff = append(diff, mkDiff(%[4]s, %[2]s, %[3]s))\n%[1]s}\n",
				prefix, pathA, pathB, diffPath)
			continue
		}
		s.WriteComparisons(w, prefix, usePrefix)
		switch {
		case hasIf:
			prefix = prefix[:len(prefix)-1]
			fmt.Fprintf(w, prefix+"}\n")
		}
	}
	if !*methods || len(c.Methods) == 0 {
		return
	}
	pathB := c.MakePath("b.", (c.path))
	fmt.Fprintf(w, "%[1]sif %[2]s != nil {\n",
		prefix, pathB)
	for _, m := range c.Methods {
		pathB := c.MakePath("b.", (c.path))
		p := make([]string, 0, len(c.path)+1)
		for _, item := range c.path {
			if item[0] != '[' {
				p = append(p, "\""+item+"\"")
			}
		}
		p = append(p, "\""+m+"\"")
		diffPath := "[]string{" + strings.Join(p, ", ") + "}"
		if usePrefix {
			diffPath = fmt.Sprintf("append(prefix, %s)", strings.Join(p, ", "))
		}
		fmt.Fprintf(w, "%[1]s\tdiff = append(diff, mkDiff(%[4]s, nil, %[3]s))\n",
			prefix, "", pathB+"."+m, diffPath)
	}
	fmt.Fprintf(w, "%[1]s}\n",
		prefix, pathB)
}

func (c *Comparisons) MakePath(start string, path []string) string {
	out := start
	for _, item := range path {
		if item == "[pointer]" {
			out = "(*" + strings.TrimRight(out, ".") + ")."
			continue
		}
		out += item + "."
	}
	return strings.TrimRight(out, ".")
}

func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}
