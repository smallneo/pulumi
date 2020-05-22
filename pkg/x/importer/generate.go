package importer

import (
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2/model"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2/syntax"
	"github.com/pulumi/pulumi/sdk/v2/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v2/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v2/go/common/util/contract"
	"github.com/zclconf/go-cty/cty"
)

type LanguageGenerator func(w io.Writer, p *hcl2.Program) error

func GenerateLanguageDefinition(w io.Writer, host plugin.Host, gen LanguageGenerator, state *resource.State) error {
	hcl2Def, err := GenerateHCL2Definition(state)
	if err != nil {
		return err
	}

	text := fmt.Sprintf("%v", hcl2Def)

	parser := syntax.NewParser()
	if err = parser.ParseFile(strings.NewReader(text), string(state.URN.Name()+".pp")); err != nil {
		return err
	}
	contract.Assert(len(parser.Diagnostics) == 0)

	program, diags, err := hcl2.BindProgram(parser.Files, hcl2.PluginHost(host))
	if err != nil {
		return err
	}
	if len(diags) != 0 {
		// TODO: do better here
		return diags[0]
	}

	return gen(w, program)
}

func GenerateHCL2Definition(state *resource.State) (*model.Block, error) {
	items := make([]model.BodyItem, 0, len(state.Inputs))
	for _, key := range state.Inputs.StableKeys() {
		// Ignore internal properties.
		if strings.HasPrefix(string(key), "__") {
			continue
		}

		x, err := generateValue(state.Inputs[key])
		if err != nil {
			return nil, err
		}
		items = append(items, &model.Attribute{
			Name:  string(key),
			Value: x,
		})
	}

	// TODO: resource options

	typ, name := state.URN.Type(), state.URN.Name()
	return &model.Block{
		Tokens: syntax.NewBlockTokens("resource", string(name), string(typ)),
		Type:   "resource",
		Labels: []string{string(name), string(typ)},
		Body: &model.Body{
			Items: items,
		},
	}, nil
}

var Null = &model.Variable{
	Name:         "null",
	VariableType: model.NoneType,
}

func generateValue(value resource.PropertyValue) (model.Expression, error) {
	switch {
	case value.IsArchive():
		return nil, fmt.Errorf("NYI: archives")
	case value.IsArray():
		arr := value.ArrayValue()
		exprs := make([]model.Expression, len(arr))
		for i, v := range arr {
			x, err := generateValue(v)
			if err != nil {
				return nil, err
			}
			exprs[i] = x
		}
		return &model.TupleConsExpression{
			Tokens:      syntax.NewTupleConsTokens(len(exprs)),
			Expressions: exprs,
		}, nil
	case value.IsAsset():
		return nil, fmt.Errorf("NYI: archives")
	case value.IsBool():
		return &model.LiteralValueExpression{
			Value: cty.BoolVal(value.BoolValue()),
		}, nil
	case value.IsComputed() || value.IsOutput():
		return nil, fmt.Errorf("cannot define computed values")
	case value.IsNull():
		return &model.ScopeTraversalExpression{
			RootName:  "null",
			Traversal: hcl.Traversal{hcl.TraverseRoot{Name: "null"}},
			Parts:     []model.Traversable{Null},
		}, nil
	case value.IsNumber():
		return &model.LiteralValueExpression{
			Value: cty.NumberFloatVal(value.NumberValue()),
		}, nil
	case value.IsObject():
		obj := value.ObjectValue()
		items := make([]model.ObjectConsItem, 0, len(obj))
		for _, k := range obj.StableKeys() {
			// Ignore internal properties.
			if strings.HasPrefix(string(k), "__") {
				continue
			}

			x, err := generateValue(obj[k])
			if err != nil {
				return nil, err
			}
			items = append(items, model.ObjectConsItem{
				Key: &model.LiteralValueExpression{
					Value: cty.StringVal(string(k)),
				},
				Value: x,
			})
		}
		return &model.ObjectConsExpression{
			Tokens: syntax.NewObjectConsTokens(len(items)),
			Items:  items,
		}, nil
	case value.IsSecret():
		arg, err := generateValue(value.SecretValue().Element)
		if err != nil {
			return nil, err
		}
		return &model.FunctionCallExpression{
			Name: "secret",
			Signature: model.StaticFunctionSignature{
				Parameters: []model.Parameter{{
					Name: "value",
					Type: arg.Type(),
				}},
				ReturnType: model.NewOutputType(arg.Type()),
			},
			Args: []model.Expression{arg},
		}, nil
	case value.IsString():
		return &model.TemplateExpression{
			Parts: []model.Expression{
				&model.LiteralValueExpression{
					Value: cty.StringVal(value.StringValue()),
				},
			},
		}, nil
	default:
		contract.Failf("unexpected property value %v", value)
		return nil, nil
	}
}
