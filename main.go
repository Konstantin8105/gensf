package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Konstantin8105/errors"
)

// result of generation:
// P is very big struct
// type P struct {
//		A int // A is some value
// }
//
// func (p P) ToHtml() (out string){
//      out += fmt.Printf("P is very big struct\n")
//      out += fmt.Printf(
//      "\n%s :<br>\n<input type=\"text\" name=\"%s\" value=\"%s\"><br>\n",
//      "A is some value","P.A", fmt.Sprintf("%v",p.A))
//      return
// }

var Parameter = struct {
	InputFilename  []string
	OutputFilename string
	Structs        []string
	PackageName    string

	// result source
	Source bytes.Buffer
}{}

var imports = map[string]bool{}

func AddImport(imp string) {
	imports[imp] = true
}

func ResetParameter() {
	Parameter.InputFilename = []string{}
	Parameter.OutputFilename = ""
	Parameter.Structs = []string{}
	Parameter.PackageName = "main"

	Parameter.Source.Reset()
}

// pipe for outpur information
var osStdout = os.Stdout

type arrayStrings []string

func (a *arrayStrings) String() string {
	return fmt.Sprintf("%v", []string(*a))
}

func (a *arrayStrings) Set(value string) error {
	v := []string(*a)
	v = append(v, value)
	*a = arrayStrings(v)
	return nil
}

func main() {
	// CLI design
	// gensf -struct=foo -struct=buz -o=out_file.go -i=file1.go -i=file2.go

	// flags
	pif := arrayStrings(Parameter.InputFilename)
	pst := arrayStrings(Parameter.Structs)

	flag.Var(&pif, "i", "input filename for example : 'main.go'")
	flag.Var(&pst, "struct", "name of struct")
	flag.StringVar(&Parameter.OutputFilename, "o", "out_gen.go", "name of output filename")
	flag.StringVar(&Parameter.PackageName, "p", "main", "package in generate file")
	flag.Parse()

	Parameter.InputFilename = []string(pif)
	Parameter.Structs = []string(pst)

	// run parsing
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(-1)
	}
}

func run() error {
	// check input data
	et := errors.New("Check input data")
	if len(Parameter.InputFilename) == 0 {
		et.Add(fmt.Errorf("input file/files is not added"))
	}
	if len(Parameter.Structs) == 0 {
		et.Add(fmt.Errorf("name of struct is not added"))
	}
	if Parameter.PackageName == "" {
		et.Add(fmt.Errorf("name of package is empty"))
	}
	if Parameter.OutputFilename == "" {
		et.Add(fmt.Errorf("name of output file is empty"))
	}
	for i := range Parameter.InputFilename {
		_, err := os.Stat(Parameter.InputFilename[i])
		if err != nil {
			et.Add(fmt.Errorf("input file `%s` is not exist", Parameter.InputFilename[i]))
		}
	}
	if et.IsError() {
		flag.PrintDefaults()
		return et
	}

	// print input data
	fmt.Fprintf(osStdout, "Generate HTML form from Go struct:\n")
	fmt.Fprintf(osStdout, "Package name: %s\n", Parameter.PackageName)
	fmt.Fprintf(osStdout, "Input go files:\n")
	for i := range Parameter.InputFilename {
		fmt.Fprintf(osStdout, "\t* %s\n", Parameter.InputFilename[i])
	}
	fmt.Fprintf(osStdout, "Parsing next Go structs:\n")
	for i := range Parameter.Structs {
		fmt.Fprintf(osStdout, "\t* %s\n", Parameter.Structs[i])
	}
	fmt.Fprintf(osStdout, "Output go file: %s\n", Parameter.OutputFilename)

	// get present folder
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Cannot get name of present folder : %v", err)
	}

	// parsing file to ast
	var files []*ast.File
	et.Name = "parsing Go files to AST"
	for _, filename := range Parameter.InputFilename {
		f, err := parser.ParseFile(
			token.NewFileSet(),
			filepath.FromSlash(pwd+"/"+filename),
			nil,
			parser.ParseComments)
		if err != nil {
			et.Add(fmt.Errorf("Cannot parse file : %s", filename)).
				Add(err)
		} else {
			files = append(files, f)
		}
	}
	if et.IsError() {
		return et
	}

	// parsing to HTML, Go
	et.Name = "Parsing go to html, html to go"

	for i := range files {
		for k := range files[i].Decls {
			decl, ok := files[i].Decls[k].(*ast.GenDecl)
			if !ok {
				continue
			}
			for j := range Parameter.Structs {
				err := parsing(decl, Parameter.Structs[j])
				if err != nil {
					et.Add(err)
					continue
				}
			}
		}
	}
	if et.IsError() {
		return et
	}

	// save structs into output file
	if _, err := os.Stat(Parameter.OutputFilename); err == nil {
		err = os.Remove(Parameter.OutputFilename)
		if err != nil {
			return fmt.Errorf("cannot remove file : %v", err)
		}
	}

	err = ioutil.WriteFile(Parameter.OutputFilename, append(header(), Parameter.Source.Bytes()...), 0644)
	if err != nil {
		return err
	}

	// simplify Go code by `gofmt`
	// error ignored, because it is not change the workflow
	_, _ = exec.Command("gofmt", "-w", Parameter.OutputFilename).Output()

	return nil
}

func parsing(decl *ast.GenDecl, structName string) (err error) {
	// check : is this ast have struct name
	if decl.Tok != token.TYPE {
		return
	}
	if len(decl.Specs) != 1 {
		return
	}
	if _, ok := decl.Specs[0].(*ast.TypeSpec); !ok {
		return
	}
	tc := decl.Specs[0].(*ast.TypeSpec)
	if tc.Name.Name != structName {
		return
	}

	// is this struct
	fl, ok := tc.Type.(*ast.StructType)
	if !ok {
		err = fmt.Errorf("Not StructType type : %T", tc.Type)
		return
	}

	// parsing by parts
	et := errors.New("Parsing errors:")
	// ToHtml : header
	Parameter.Source.WriteString(fmt.Sprintf(
		"\nfunc (value %s) toHtml(prefix string) (out string) {\n", structName))
	for _, fs := range fl.Fields.List {
		err = structToHtml(fs, structName+".")
		if err != nil {
			et.Add(err)
			continue
		}
	}
	// ToHtml : footer
	Parameter.Source.WriteString("\treturn\n")
	Parameter.Source.WriteString("}\n\n")

	Parameter.Source.WriteString(fmt.Sprintf(
		"\nfunc (value %s) ToHtml() (out string) {\n", structName))
	Parameter.Source.WriteString(fmt.Sprintf("\treturn value.toHtml(\"%s.\")\n", structName))
	Parameter.Source.WriteString("}\n\n")

	for _, fs := range fl.Fields.List {
		// ToStruct
		err = HtmlToStruct(fs, structName)
		if err != nil {
			et.Add(err)
			continue
		}
	}

	// ToForm
	err = createForm(structName)
	if err != nil {
		et.Add(err)
	}

	if et.IsError() {
		err = et
	}

	return
}

type field struct {
	// asd.ert.qwe
	StructName              string // asd
	FieldName               string // .ert.qwe
	FieldNameWithFirstPoint string //  ert.qwe

	Docs string
}

func (f *field) Parse(a *ast.Field, structName string) (err error) {
	if len(a.Names) != 1 {
		// Panic with debug information for understood
		err = fmt.Errorf("Too many names\n")
		return
	}

	f.FieldName = a.Names[0].Name

	if a.Doc != nil {
		for i := 0; i < len(a.Doc.List); i++ {
			f.Docs += a.Doc.List[i].Text[2:] // [2:] for remove words:"//","/*"
		}
		f.Docs = strings.TrimSpace(f.Docs)
	}
	// not allowable empty documentation
	if len(f.Docs) == 0 {
		// if docs is empty
		fmt.Fprintf(os.Stderr, "Struct `%s` haven`t documentation\n", structName)
	}

	f.Docs = strconv.Quote(f.Docs)
	if len(f.Docs) >= 2 {
		f.Docs = f.Docs[1 : len(f.Docs)-1]
	}
	f.FieldNameWithFirstPoint = structName + f.FieldName
	index := strings.Index(f.FieldNameWithFirstPoint, ".")
	if index < 0 {
		return fmt.Errorf("cannot find point in : %s", f.FieldNameWithFirstPoint)
	}
	f.StructName = f.FieldNameWithFirstPoint[:index] + "."
	f.FieldNameWithFirstPoint = f.FieldNameWithFirstPoint[index:]
	f.FieldName = f.FieldNameWithFirstPoint[1:]

	return nil
}

func header() (b []byte) {
	var buf bytes.Buffer

	// general comment
	buf.WriteString("// Code generated by gensf. DO NOT EDIT.\n\n")

	// header
	buf.WriteString("package main\n\n")

	// add imports
	for k := range imports {
		buf.WriteString(fmt.Sprintf("import \"%s\"\n", k))
	}
	// buf.WriteString("import \"fmt\"\n" +
	// 	"import \"strconv\"\n" +
	// 	"import errors \"github.com/Konstantin8105/errors\"\n" +
	// 	"import \"net/http\"\n\n")

	return buf.Bytes()
}
