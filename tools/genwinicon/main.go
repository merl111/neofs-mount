// genwinicon extracts the tray PNG from cmd/neofs-mount-tray/bundled.go and writes app.ico
// for Windows PE embedding (see rsrc / go generate in the tray package).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/png"
	"os"
	"strconv"

	ico "github.com/Kodeworks/golang-image-ico"
)

func main() {
	bundled := flag.String("bundled", "bundled.go", "path to bundled.go (tray package)")
	out := flag.String("o", "app.ico", "output .ico path")
	flag.Parse()

	pngBytes, err := pngFromBundled(*bundled)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genwinicon:", err)
		os.Exit(1)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		fmt.Fprintln(os.Stderr, "genwinicon: decode png:", err)
		os.Exit(1)
	}
	var buf bytes.Buffer
	if err := ico.Encode(&buf, img); err != nil {
		fmt.Fprintln(os.Stderr, "genwinicon: encode ico:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "genwinicon: write:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}

func pngFromBundled(path string) ([]byte, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, decl := range node.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				if id.Name != "resourceLogoPng" || i >= len(vs.Values) {
					continue
				}
				u, ok := vs.Values[i].(*ast.UnaryExpr)
				if !ok || u.Op != token.AND {
					continue
				}
				cl, ok := u.X.(*ast.CompositeLit)
				if !ok {
					continue
				}
				b, err := staticContentBytes(cl)
				if err != nil {
					return nil, err
				}
				if b != nil {
					return b, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("resourceLogoPng not found in %s", path)
}

func staticContentBytes(cl *ast.CompositeLit) ([]byte, error) {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok || k.Name != "StaticContent" {
			continue
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return nil, fmt.Errorf("StaticContent: expected []byte(...)")
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return nil, fmt.Errorf("StaticContent: expected string literal")
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, fmt.Errorf("unquote StaticContent: %w", err)
		}
		return []byte(s), nil
	}
	return nil, nil
}
