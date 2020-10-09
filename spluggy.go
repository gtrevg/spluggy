package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ExportedFunction struct {
	Name      string
	Deps      []string
	Signature string
}

func process_source(src string) []ExportedFunction {
	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		log.Fatalf("Failed to parse source: %+v", err)
		return nil
	}

	// imported name to package
	imports := make(map[string]string)

	for _, i := range node.Imports {
		path := strings.Trim(i.Path.Value, "\"")
		parts := strings.Split(path, "/")
		imports[parts[len(parts)-1]] = path
	}
	log_Debug("Imports: %+v\n", imports)

	funcs := make([]ExportedFunction, 0, len(node.Decls))

	for _, d := range node.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !f.Name.IsExported() {
			continue
		}

		var fn ExportedFunction
		fn.Name = f.Name.Name

		deps := make([]string, 0)
		added := make(map[string]int)
		fn.Signature = "("
		if f.Type.Params.List != nil {
			for _, param := range f.Type.Params.List {
				dtype := src[param.Type.Pos()-1 : param.Type.End()-1]
				fn.Signature += dtype + ", "
				part := strings.Split(dtype, ".")[0]
				part = strings.Trim(part, "[]*")
				dep, found := imports[part]
				if !found || added[dep] == 1 {
					continue
				}
				added[dep] = 1
				log_Debug("appending dep: %s (%s)\n", part, dtype)
				deps = append(deps, dep)
			}
		}
		fn.Signature = strings.TrimSuffix(fn.Signature, ", ") + ")"

		if f.Type.Results != nil {
			fn.Signature += "("
			for _, param := range f.Type.Results.List {
				dtype := src[param.Type.Pos()-1 : param.Type.End()-1]
				fn.Signature += dtype + ", "
				part := strings.Split(dtype, ".")[0]
				part = strings.Trim(part, "[]*")
				dep, found := imports[part]
				if !found || added[dep] == 1 {
					continue
				}
				added[dep] = 1
				log_Debug("appending dep: %s (%s)\n", part, dtype)
				deps = append(deps, dep)
			}
			fn.Signature = strings.TrimSuffix(fn.Signature, ", ") + ")"
		}
		fn.Deps = deps

		funcs = append(funcs, fn)
	}

	return funcs
}

var argfuncname = flag.String("func", "", "The interface function name")
var argbasepkg = flag.String("pkg", "", "The base package")
var argoutfname = flag.String("out", "plugins.go", "Output file name")
var argverbose = flag.Bool("v", false, "Flag to enable verbose output")

func log_Debug(fmtstr string, vars ...interface{}) {
	if !*argverbose {
		return
	}
	log.Printf("[DEBUG] "+fmtstr, vars...)
}

func main() {
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		log.Fatalf("Wrong number of arguments. You need to specify a directory")
	}

	base := args[0]
	if strings.HasPrefix(base, "./") {
		base = base[2:]
	}
	pkgfuncs := make(map[string][]ExportedFunction)
	filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Fatalf(err.Error())
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		buf, _ := ioutil.ReadFile(path)
		src := string(buf)
		parts := strings.Split(path[len(base):], "/")
		//parts := strings.Split(path, "/")
		pkg := strings.Join(parts[0:len(parts)-1], "/")
		pkg = strings.Trim(pkg, "/.")
		if pkg == "" {
			return nil
		}
		log_Debug("About to process file %s (pkg %s)\n", path, pkg)
		funcs := process_source(src)
		log_Debug("resulted functions: %+v\n", funcs)
		existingfuncs, found := pkgfuncs[pkg]
		if !found {
			existingfuncs = nil
		}
		pkgfuncs[pkg] = append(existingfuncs, funcs...)
		return nil
	})

	log_Debug("pkgfuncs: %+v\n", pkgfuncs)

	funcocc := make(map[string]int)
	for _, fns := range pkgfuncs {
		for _, fn := range fns {
			if len(*argfuncname) > 0 && fn.Name != *argfuncname {
				continue
			}
			n, found := funcocc[fn.Name]
			if !found {
				n = 0
			}
			funcocc[fn.Name] = n + 1
		}
	}
	cands := make([]string, 0, len(funcocc))
	for funcname, n := range funcocc {
		if n == len(pkgfuncs) {
			cands = append(cands, funcname)
		}
	}

	log_Debug("cands: %+v\n", cands)
	if len(cands) == 0 {
		flag.Usage()
		log.Fatalf("Cannot find any common public function in all packages")
	}
	if len(cands) > 1 {
		flag.Usage()
		log.Fatalf("Multiple common public functions, specify one with -func.")
	}

	fname := cands[0]

	var fn ExportedFunction
	for _, fns := range pkgfuncs {
		for _, f := range fns {
			if f.Name == fname {
				fn = f
				break
			}
		}
		break
	}

	log_Debug("interface function is %s: %+v\n", fname, fn)

	code := make([]string, 0, len(pkgfuncs)+16)

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	code = append(code, fmt.Sprintf("// Generated by spluggy on %s", timestamp))
	code = append(code, "package plugins")

	for _, dep := range fn.Deps {
		code = append(code, fmt.Sprintf("import \"%s\"", dep))
	}

	i := 0
	pkg2importname := make(map[string]string)
	for pkg, _ := range pkgfuncs {
		p := pkg
		if len(*argbasepkg) > 0 {
			p = *argbasepkg + "/" + p
		}
		importname := fmt.Sprintf("p%d", i)
		pkg2importname[pkg] = importname
		code = append(code, fmt.Sprintf("import %s \"%s\"", importname, p))
		i = i + 1
	}

	code = append(code, fmt.Sprintf("\ntype Function func%s\n", fn.Signature))

	code = append(code, "func Plugins() map[string]Function {\n")
	code = append(code, "\tplugins := make(map[string]Function)\n")

	for pkg, _ := range pkgfuncs {
		code = append(code, fmt.Sprintf("\tplugins[\"%s\"] = %s.%s", pkg, pkg2importname[pkg], fn.Name))
	}

	code = append(code, "\n\treturn plugins\n}\n")

	out := fmt.Sprintf("%s/%s", base, *argoutfname)
	err := ioutil.WriteFile(out, []byte(strings.Join(code, "\n")), 0644)
	if err != nil {
		log.Fatalf("Failed to wrote code to %s: %+v", out, err)
	}
	log_Debug("Plugins definition written to %s\n", out)
}
