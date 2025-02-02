package gen

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2/model"
	"github.com/pulumi/pulumi/pkg/v2/codegen/schema"
	"github.com/pulumi/pulumi/sdk/v2/go/common/util/contract"
	"github.com/zclconf/go-cty/cty"
)

func (g *generator) GetPrecedence(expr model.Expression) int {
	// TODO: Current values copied from Node, update based on
	// https://golang.org/ref/spec
	switch expr := expr.(type) {
	case *model.ConditionalExpression:
		return 4
	case *model.BinaryOpExpression:
		switch expr.Operation {
		case hclsyntax.OpLogicalOr:
			return 5
		case hclsyntax.OpLogicalAnd:
			return 6
		case hclsyntax.OpEqual, hclsyntax.OpNotEqual:
			return 11
		case hclsyntax.OpGreaterThan, hclsyntax.OpGreaterThanOrEqual, hclsyntax.OpLessThan,
			hclsyntax.OpLessThanOrEqual:
			return 12
		case hclsyntax.OpAdd, hclsyntax.OpSubtract:
			return 14
		case hclsyntax.OpMultiply, hclsyntax.OpDivide, hclsyntax.OpModulo:
			return 15
		default:
			contract.Failf("unexpected binary expression %v", expr)
		}
	case *model.UnaryOpExpression:
		return 17
	case *model.FunctionCallExpression:
		switch expr.Name {
		default:
			return 20
		}
	case *model.ForExpression, *model.IndexExpression, *model.RelativeTraversalExpression, *model.SplatExpression,
		*model.TemplateJoinExpression:
		return 20
	case *model.AnonymousFunctionExpression, *model.LiteralValueExpression, *model.ObjectConsExpression,
		*model.ScopeTraversalExpression, *model.TemplateExpression, *model.TupleConsExpression:
		return 22
	default:
		contract.Failf("unexpected expression %v of type %T", expr, expr)
	}
	return 0
}

// GenAnonymousFunctionExpression generates code for an AnonymousFunctionExpression.
func (g *generator) GenAnonymousFunctionExpression(w io.Writer, expr *model.AnonymousFunctionExpression) {
	g.Fgenf(w, "func(")
	leadingSep := ""
	for _, param := range expr.Signature.Parameters {
		isInput := isInputty(param.Type)
		g.Fgenf(w, "%s%s %s", leadingSep, param.Name, argumentTypeName(nil, param.Type, isInput))
		leadingSep = ", "
	}

	isInput := isInputty(expr.Signature.ReturnType)
	retType := argumentTypeName(nil, expr.Signature.ReturnType, isInput)
	g.Fgenf(w, ") (%s, error) {\n", retType)
	g.Fgenf(w, "return %v, nil", expr.Body)
	g.Fgenf(w, "\n}")
}

func (g *generator) GenBinaryOpExpression(w io.Writer, expr *model.BinaryOpExpression) {
	opstr, precedence := "", g.GetPrecedence(expr)
	switch expr.Operation {
	case hclsyntax.OpAdd:
		opstr = "+"
	case hclsyntax.OpDivide:
		opstr = "/"
	case hclsyntax.OpEqual:
		opstr = "=="
	case hclsyntax.OpGreaterThan:
		opstr = ">"
	case hclsyntax.OpGreaterThanOrEqual:
		opstr = ">="
	case hclsyntax.OpLessThan:
		opstr = "<"
	case hclsyntax.OpLessThanOrEqual:
		opstr = "<="
	case hclsyntax.OpLogicalAnd:
		opstr = "&&"
	case hclsyntax.OpLogicalOr:
		opstr = "||"
	case hclsyntax.OpModulo:
		opstr = "%"
	case hclsyntax.OpMultiply:
		opstr = "*"
	case hclsyntax.OpNotEqual:
		opstr = "!="
	case hclsyntax.OpSubtract:
		opstr = "-"
	default:
		opstr, precedence = ",", 1
	}

	g.Fgenf(w, "%.[1]*[2]v %[3]v %.[1]*[4]o", precedence, expr.LeftOperand, opstr, expr.RightOperand)
}

func (g *generator) GenConditionalExpression(w io.Writer, expr *model.ConditionalExpression) {
	// Ternary expressions are not supported in go so we need to allocate temp variables in the parent scope.
	// This is handled by lower expression and rewriteTernaries
	contract.Failf("unlowered conditional expression @ %v", expr.SyntaxNode().Range())
}

// GenForExpression generates code for a ForExpression.
func (g *generator) GenForExpression(w io.Writer, expr *model.ForExpression) { /*TODO*/ }

func (g *generator) GenFunctionCallExpression(w io.Writer, expr *model.FunctionCallExpression) {
	switch expr.Name {
	case hcl2.IntrinsicInput:
		isInput := true
		switch arg := expr.Args[0].(type) {
		case *model.ScopeTraversalExpression:
			g.genScopeTraversalExpression(w, arg, isInput)
		default:
			argType := argumentTypeName(arg, arg.Type(), isInput)
			g.Fgenf(w, "%s(%v", argType, arg)
			g.Fgenf(w, ")")
		}
	case hcl2.IntrinsicConvert:
		switch arg := expr.Args[0].(type) {
		case *model.TupleConsExpression:
			g.genTupleConsExpression(w, arg, expr.Type())
		case *model.ObjectConsExpression:
			g.genObjectConsExpression(w, arg, expr.Type())
		case *model.LiteralValueExpression:
			g.genLiteralValueExpression(w, arg, expr.Type())
		default:
			g.Fgenf(w, "%.v", expr.Args[0]) // <- probably wrong w.r.t. precedence
		}
	case hcl2.IntrinsicApply:
		g.genApply(w, expr)
	// case intrinsicAwait:
	// g.genNYI(w, "call %v", expr.Name)
	// g.Fgenf(w, "await %.17v", expr.Args[0])
	// case intrinsicOutput:
	// g.Fgenf(w, "Output.Create(%.v)", expr.Args[0])
	case "element":
		g.genNYI(w, "element")
	case "entries":
		g.genNYI(w, "call %v", expr.Name)
		// switch model.ResolveOutputs(expr.Args[0].Type()).(type) {
		// case *model.ListType, *model.TupleType:
		// 	if call, ok := expr.Args[0].(*model.FunctionCallExpression); ok && call.Name == "range" {
		// 		g.genRange(w, call, true)
		// 		return
		// 	}
		// 	g.Fgenf(w, "%.20v.Select((v, k)", expr.Args[0])
		// case *model.MapType, *model.ObjectType:
		// 	g.genNYI(w, "MapOrObjectEntries")
		// }
		// g.Fgenf(w, " => new { Key = k, Value = v })")
	case "fileArchive":
		g.genNYI(w, "call %v", expr.Name)
		// g.Fgenf(w, "new FileArchive(%.v)", expr.Args[0])
	case "fileAsset":
		g.genNYI(w, "call %v", expr.Name)
		// g.Fgenf(w, "new FileAsset(%.v)", expr.Args[0])
	case hcl2.Invoke:
		_, module, fn, diags := functionName(expr.Args[0])
		contract.Assert(len(diags) == 0)
		name := fmt.Sprintf("%s.%s", module, fn)

		optionsBag := ""
		var buf bytes.Buffer
		if len(expr.Args) == 3 {
			g.Fgenf(&buf, ", %.v", expr.Args[2])
		} else {
			g.Fgenf(&buf, ", nil")
		}
		optionsBag = buf.String()

		g.Fgenf(w, "%s(ctx, ", name)
		g.Fgenf(w, "%.v", expr.Args[1])
		g.Fgenf(w, "%v)", optionsBag)
	case "length":
		g.Fgenf(w, "%.20v.Length", expr.Args[0])
	case "lookup":
		g.genNYI(w, "Lookup")
	case "range":
		g.genNYI(w, "call %v", expr.Name)
		// g.genRange(w, expr, false)
	case "readFile":
		g.genNYI(w, "ReadFile")
	case "readDir":
		// TODO
		g.genNYI(w, "call %v", expr.Name)
		// C# for reference
		// g.Fgenf(w, "Directory.GetFiles(%.v).Select(Path.GetFileName)", expr.Args[0])
	case "split":
		g.Fgenf(w, "%.20v.Split(%v)", expr.Args[1], expr.Args[0])
	case "toJSON":
		contract.Failf("unlowered toJSON function expression @ %v", expr.SyntaxNode().Range())
	default:
		g.genNYI(w, "call %v", expr.Name)
	}
}

// GenIndexExpression generates code for an IndexExpression.
func (g *generator) GenIndexExpression(w io.Writer, expr *model.IndexExpression) { /*TODO*/ }

func (g *generator) GenLiteralValueExpression(w io.Writer, expr *model.LiteralValueExpression) {
	g.genLiteralValueExpression(w, expr, expr.Type())
}

func (g *generator) genLiteralValueExpression(w io.Writer, expr *model.LiteralValueExpression, destType model.Type) {
	argTypeName := argumentTypeName(expr, destType, false)
	isPulumiType := strings.HasPrefix(argTypeName, "pulumi.")

	switch destType := destType.(type) {
	case *model.OpaqueType:
		switch destType {
		case model.BoolType:
			if isPulumiType {
				g.Fgenf(w, "%s(%v)", argTypeName, expr.Value.True())
			} else {
				g.Fgenf(w, "%v", expr.Value.True())
			}
		case model.NumberType, model.IntType:
			bf := expr.Value.AsBigFloat()
			if i, acc := bf.Int64(); acc == big.Exact {
				if isPulumiType {
					g.Fgenf(w, "%s(%d)", argTypeName, i)
				} else {
					g.Fgenf(w, "%d", i)
				}

			} else {
				f, _ := bf.Float64()
				if isPulumiType {
					g.Fgenf(w, "%s(%g)", argTypeName, f)
				} else {
					g.Fgenf(w, "%g", f)
				}
			}
		case model.StringType:
			strVal := expr.Value.AsString()
			if isPulumiType {
				g.Fgenf(w, "%s(", argTypeName)
				g.genStringLiteral(w, strVal)
				g.Fgenf(w, ")")
			} else {
				g.genStringLiteral(w, strVal)
			}
		default:
			contract.Failf("unexpected opaque type in GenLiteralValueExpression: %v (%v)", destType,
				expr.SyntaxNode().Range())
		}
	// handles the __convert intrinsic assuming that the union type will have an opaque type containing the dest type
	case *model.UnionType:
		for _, t := range destType.ElementTypes {
			switch t := t.(type) {
			case *model.OpaqueType:
				g.genLiteralValueExpression(w, expr, t)
				break
			}
		}
	default:
		contract.Failf("unexpected destType in GenLiteralValueExpression: %v (%v)", destType,
			expr.SyntaxNode().Range())
	}
}

func (g *generator) GenObjectConsExpression(w io.Writer, expr *model.ObjectConsExpression) {
	g.genObjectConsExpression(w, expr, expr.Type())
}

func (g *generator) genObjectConsExpression(w io.Writer, expr *model.ObjectConsExpression, destType model.Type) {
	if len(expr.Items) > 0 {
		var temps []interface{}
		isInput := isInputty(destType)
		typeName := argumentTypeName(expr, destType, isInput)
		if strings.HasSuffix(typeName, "Args") {
			isInput = true
		}
		// invokes are not inputty
		if strings.Contains(typeName, ".Lookup") {
			isInput = false
		}
		isMap := strings.HasPrefix(typeName, "map[")

		// TODO: retrieve schema and propagate optionals to emit bool ptr, etc.

		// first lower all inner expressions and emit temps
		for i, item := range expr.Items {
			// don't treat keys as inputs
			k, kTemps := g.lowerExpression(item.Key, item.Key.Type(), false)
			temps = append(temps, kTemps...)
			item.Key = k
			x, xTemps := g.lowerExpression(item.Value, item.Value.Type(), isInput)
			temps = append(temps, xTemps...)
			item.Value = x
			expr.Items[i] = item
		}
		g.genTemps(w, temps)

		if isMap {
			g.Fgenf(w, "%s", typeName)
		} else {
			g.Fgenf(w, "&%s", typeName)
		}
		g.Fgenf(w, "{\n")

		for _, item := range expr.Items {
			if lit, ok := g.literalKey(item.Key); ok {
				if isMap {
					g.Fgenf(w, "\"%s\"", lit)
				} else {
					g.Fgenf(w, "%s", Title(lit))
				}
			} else {
				g.Fgenf(w, "%.v", item.Key)
			}

			g.Fgenf(w, ": %.v,\n", item.Value)
		}

		g.Fgenf(w, "}")
	}
}

func (g *generator) GenRelativeTraversalExpression(w io.Writer, expr *model.RelativeTraversalExpression) {
	g.Fgenf(w, "%.20v", expr.Source)
	g.genRelativeTraversal(w, expr.Traversal, expr.Parts, nil)
}

func (g *generator) GenScopeTraversalExpression(w io.Writer, expr *model.ScopeTraversalExpression) {
	isInput := false
	g.genScopeTraversalExpression(w, expr, isInput)
}

func (g *generator) genScopeTraversalExpression(w io.Writer, expr *model.ScopeTraversalExpression, isInput bool) {
	rootName := expr.RootName
	// TODO splat
	// if _, ok := expr.Parts[0].(*model.SplatVariable); ok {
	// 	rootName = "__item"
	// }

	genIDCall := false

	var objType *schema.ObjectType
	if resource, ok := expr.Parts[0].(*hcl2.Resource); ok {
		isInput = false
		if schemaType, ok := hcl2.GetSchemaForType(resource.InputType); ok {
			objType, _ = schemaType.(*schema.ObjectType)
			// convert .id into .ID()
			last := expr.Traversal[len(expr.Traversal)-1]
			if attr, ok := last.(hcl.TraverseAttr); ok && attr.Name == "id" {
				genIDCall = true
				expr.Traversal = expr.Traversal[:len(expr.Traversal)-1]
			}
		}
	}

	// TODO if it's an array type, we need a lowering step to turn []string -> pulumi.StringArray
	if isInput {
		g.Fgenf(w, "%s(", argumentTypeName(expr, expr.Type(), isInput))
	}
	g.Fgen(w, rootName)
	g.genRelativeTraversal(w, expr.Traversal.SimpleSplit().Rel, expr.Parts, objType)

	if isInput {
		g.Fgenf(w, ")")
	}

	if genIDCall {
		g.Fgenf(w, ".ID()")
	}
}

// GenSplatExpression generates code for a SplatExpression.
func (g *generator) GenSplatExpression(w io.Writer, expr *model.SplatExpression) { /*TODO*/ }

// GenTemplateExpression generates code for a TemplateExpression.
func (g *generator) GenTemplateExpression(w io.Writer, expr *model.TemplateExpression) {
	if len(expr.Parts) == 1 {
		if lit, ok := expr.Parts[0].(*model.LiteralValueExpression); ok && lit.Type() == model.StringType {
			g.GenLiteralValueExpression(w, lit)
			return
		}
	}

	g.genNYI(w, "TODO multi part template expressions")
}

// GenTemplateJoinExpression generates code for a TemplateJoinExpression.
func (g *generator) GenTemplateJoinExpression(w io.Writer, expr *model.TemplateJoinExpression) { /*TODO*/
}

func (g *generator) GenTupleConsExpression(w io.Writer, expr *model.TupleConsExpression) {
	g.genTupleConsExpression(w, expr, expr.Type())
}

// GenTupleConsExpression generates code for a TupleConsExpression.
func (g *generator) genTupleConsExpression(w io.Writer, expr *model.TupleConsExpression, destType model.Type) {
	isInput := isInputty(destType) || containsInputs(expr)

	var temps []interface{}
	for i, item := range expr.Expressions {
		item, itemTemps := g.lowerExpression(item, item.Type(), isInput)
		temps = append(temps, itemTemps...)
		expr.Expressions[i] = item
	}
	g.genTemps(w, temps)
	argType := argumentTypeName(expr, destType, isInput)
	g.Fgenf(w, "%s{\n", argType)
	switch len(expr.Expressions) {
	case 0:
		// empty array
		break
	default:
		for _, v := range expr.Expressions {
			g.Fgenf(w, "%v,\n", v)
		}
	}
	g.Fgenf(w, "}")
}

func (g *generator) GenUnaryOpExpression(w io.Writer, expr *model.UnaryOpExpression) {
	opstr, precedence := "", g.GetPrecedence(expr)
	switch expr.Operation {
	case hclsyntax.OpLogicalNot:
		opstr = "!"
	case hclsyntax.OpNegate:
		opstr = "-"
	}
	g.Fgenf(w, "%[2]v%.[1]*[3]v", precedence, opstr, expr.Operand)
}

// argumentTypeName computes the go type for the given expression and model type.
func argumentTypeName(expr model.Expression, destType model.Type, isInput bool) string {
	var tokenRange hcl.Range
	if expr != nil {
		node := expr.SyntaxNode()
		if node != nil && !reflect.ValueOf(node).IsNil() {
			tokenRange = expr.SyntaxNode().Range()
		}
	}
	if schemaType, ok := hcl2.GetSchemaForType(destType.(model.Type)); ok {
		switch schemaType := schemaType.(type) {
		case *schema.ArrayType:
			token := schemaType.ElementType.(*schema.ObjectType).Token
			_, module, member, diags := hcl2.DecomposeToken(token, tokenRange)
			importPrefix := strings.Split(module, "/")[0]
			contract.Assert(len(diags) == 0)
			fmtString := "[]%s.%s"
			if isInput {
				fmtString = "%s.%sArray"
			}
			return fmt.Sprintf(fmtString, importPrefix, member)
		case *schema.ObjectType:
			token := schemaType.Token
			_, module, member, diags := hcl2.DecomposeToken(token, tokenRange)
			importPrefix := strings.Split(module, "/")[0]
			contract.Assert(len(diags) == 0)
			member = Title(member)
			if strings.HasPrefix(member, "Get") {
				member = strings.Replace(member, "Get", "Lookup", 1)
				return fmt.Sprintf("%s.%s", importPrefix, member)
			}
			fmtString := "%s.%s"
			if isInput {
				fmtString = "%s.%sArgs"
			}
			return fmt.Sprintf(fmtString, importPrefix, member)
		default:
			contract.Failf("unexpected schema type %T", schemaType)
		}
	}

	switch destType := destType.(type) {
	case *model.OpaqueType:
		switch destType {
		case model.IntType:
			if isInput {
				return "pulumi.Int"
			}
			return "int"
		case model.NumberType:
			if isInput {
				return "pulumi.Float64"
			}
			return "float64"
		case model.StringType:
			if isInput {
				return "pulumi.String"
			}
			return "string"
		case model.BoolType:
			if isInput {
				return "pulumi.Bool"
			}
			return "bool"
		default:
			return destType.Name
		}
	case *model.ObjectType:
		return "map[string]interface{}"
	case *model.MapType:
		valType := argumentTypeName(nil, destType.ElementType, isInput)
		return fmt.Sprintf("map[string]%s", valType)
	case *model.ListType:
		argTypeName := argumentTypeName(nil, destType.ElementType, isInput)
		if strings.HasPrefix(argTypeName, "pulumi.") {
			return fmt.Sprintf("%sArray", argTypeName)
		}
		return fmt.Sprintf("[]%s", argTypeName)
	case *model.TupleType:
		// attempt to collapse tuple types. intentionally does not use model.UnifyTypes
		// correct go code requires all types to match, or use of interface{}
		var elmType model.Type
		for i, t := range destType.ElementTypes {
			if i == 0 {
				elmType = t
			}

			if !elmType.Equals(t) {
				elmType = nil
				break
			}
		}

		if elmType != nil {
			argTypeName := argumentTypeName(nil, elmType, isInput)
			if strings.HasPrefix(argTypeName, "pulumi.") {
				return fmt.Sprintf("%sArray", argTypeName)
			}
			return fmt.Sprintf("[]%s", argTypeName)
		}

		if isInput {
			return "pulumi.Array"
		}
		return "[]interface{}"
	case *model.OutputType:
		isInput = true
		return argumentTypeName(expr, destType.ElementType, isInput)
	case *model.UnionType:
		for _, ut := range destType.ElementTypes {
			if _, isOpaqueType := ut.(*model.OpaqueType); isOpaqueType {
				return argumentTypeName(expr, ut, isInput)
			}
		}
		return "interface{}"
	case *model.PromiseType:
		return argumentTypeName(expr, destType.ElementType, isInput)
	default:
		contract.Failf("unexpected destType type %T", destType)
	}
	return ""
}

func (g *generator) genRelativeTraversal(w io.Writer,
	traversal hcl.Traversal, parts []model.Traversable, objType *schema.ObjectType) {

	for _, part := range traversal {
		var key cty.Value
		switch part := part.(type) {
		case hcl.TraverseAttr:
			key = cty.StringVal(part.Name)
		case hcl.TraverseIndex:
			key = part.Key
		default:
			contract.Failf("unexpected traversal part of type %T (%v)", part, part.SourceRange())
		}

		// TODO handle optionals in go
		// if model.IsOptionalType(model.GetTraversableType(parts[i])) {
		// 	g.Fgen(w, "?")
		// }

		switch key.Type() {
		case cty.String:
			g.Fgenf(w, ".%s", Title(key.AsString()))
		case cty.Number:
			idx, _ := key.AsBigFloat().Int64()
			g.Fgenf(w, "[%d]", idx)
		default:
			contract.Failf("unexpected traversal key of type %T (%v)", key, key.AsString())
		}
	}
}

type nameInfo int

func (nameInfo) Format(name string) string {
	// TODO
	return name
}

// lowerExpression amends the expression with intrinsics for C# generation.
func (g *generator) lowerExpression(expr model.Expression, typ model.Type, isInput bool) (
	model.Expression, []interface{}) {
	expr, tTemps, ternDiags := g.rewriteTernaries(expr, g.ternaryTempSpiller)
	expr, jTemps, jsonDiags := g.rewriteToJSON(expr, g.jsonTempSpiller)
	expr, diags := hcl2.RewriteApplies(expr, nameInfo(0), false /*TODO*/)
	expr = hcl2.RewriteConversions(expr, typ)

	if isInput {
		expr = rewriteInputs(expr)
	}
	var temps []interface{}
	for _, t := range tTemps {
		temps = append(temps, t)
	}
	for _, t := range jTemps {
		temps = append(temps, t)
	}
	diags = append(diags, ternDiags...)
	diags = append(diags, jsonDiags...)
	contract.Assert(len(diags) == 0)
	return expr, temps
}

func (g *generator) genNYI(w io.Writer, reason string, vs ...interface{}) {
	message := fmt.Sprintf("not yet implemented: %s", fmt.Sprintf(reason, vs...))
	g.diagnostics = append(g.diagnostics, &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  message,
		Detail:   message,
	})
	g.Fgenf(w, "\"TODO: %s\"", fmt.Sprintf(reason, vs...))
}

func (g *generator) genApply(w io.Writer, expr *model.FunctionCallExpression) {
	// Extract the list of outputs and the continuation expression from the `__apply` arguments.
	applyArgs, then := hcl2.ParseApplyCall(expr)
	then = stripInputs(then).(*model.AnonymousFunctionExpression)
	isInput := false
	retType := argumentTypeName(nil, then.Signature.ReturnType, isInput)
	// TODO account for outputs in other namespaces like aws
	typeAssertion := fmt.Sprintf(".(pulumi.%sOutput)", Title(retType))

	if len(applyArgs) == 1 {
		// If we only have a single output, just generate a normal `.Apply`
		g.Fgenf(w, "%.v.ApplyT(%.v)%s", applyArgs[0], then, typeAssertion)
	} else {
		// TODO
	}
}

func (g *generator) genStringLiteral(w io.Writer, v string) {
	// TODO more robust and go-specific handling of strings
	newlines := strings.Contains(v, "\n")
	if !newlines {
		// This string does not contain newlines so we'll generate a regular string literal. Quotes and backslashes
		// will be escaped in conformance with
		// https://golang.org/ref/spec#String_literals
		g.Fgen(w, "\"")
		g.Fgen(w, g.escapeString(v))
		g.Fgen(w, "\"")
	} else {
		g.genNYI(w, "TODO multiline strings")
	}
}

func (g *generator) escapeString(v string) string {
	builder := strings.Builder{}
	for _, c := range v {
		if c == '"' || c == '\\' {
			builder.WriteRune('\\')
		}
		builder.WriteRune(c)
	}
	return builder.String()
}

// nolint: lll
func isInputty(destType model.Type) bool {
	// TODO this needs to be more robust, likely the inverse of:
	// https://github.com/pulumi/pulumi/blob/5330c97684cad78bcc60d8867f1b28704bd8a555/pkg/codegen/hcl2/model/type_eventuals.go#L244
	switch destType := destType.(type) {
	case *model.UnionType:
		for _, t := range destType.ElementTypes {
			if _, ok := t.(*model.OutputType); ok {
				return true
			}
		}
	case *model.OutputType:
		return true
	}
	return false
}

func (g *generator) literalKey(x model.Expression) (string, bool) {
	strKey := ""
	switch x := x.(type) {
	case *model.LiteralValueExpression:
		if x.Type() == model.StringType {
			strKey = x.Value.AsString()
			break
		}
		var buf bytes.Buffer
		g.GenLiteralValueExpression(&buf, x)
		return buf.String(), true
	case *model.TemplateExpression:
		if len(x.Parts) == 1 {
			if lit, ok := x.Parts[0].(*model.LiteralValueExpression); ok && lit.Type() == model.StringType {
				strKey = lit.Value.AsString()
				break
			}
		}
		var buf bytes.Buffer
		g.GenTemplateExpression(&buf, x)
		return buf.String(), true
	default:
		return "", false
	}

	return strKey, true
}

// functionName computes the go package, module, and name for the given function token.
func functionName(tokenArg model.Expression) (string, string, string, hcl.Diagnostics) {
	token := tokenArg.(*model.TemplateExpression).Parts[0].(*model.LiteralValueExpression).Value.AsString()
	tokenRange := tokenArg.SyntaxNode().Range()

	// Compute the resource type from the Pulumi type token.
	pkg, module, member, diagnostics := hcl2.DecomposeToken(token, tokenRange)
	// GetResource -> LookupResource
	// TODO figure out how to deal with get/lookup ambiguity
	// https://github.com/pulumi/pulumi/blob/adfa8116fbf61ad328568fe4b53e32c751f30553/pkg/codegen/go/gen.go#L1171
	if strings.HasPrefix(member, "get") {
		member = strings.Replace(member, "get", "lookup", 1)
	}
	return pkg, strings.Replace(module, "/", ".", -1), Title(member), diagnostics
}

var functionPackages = map[string][]string{
	"toJSON": {"encoding/json"},
}

func (g *generator) genFunctionPackages(x *model.FunctionCallExpression) []string {
	return functionPackages[x.Name]
}
