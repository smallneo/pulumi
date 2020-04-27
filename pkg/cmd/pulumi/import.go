// Copyright 2016-2018, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/pulumi/pulumi/pkg/v2/backend/display"
	"github.com/pulumi/pulumi/pkg/v2/codegen/hcl2"
	"github.com/pulumi/pulumi/pkg/v2/codegen/nodejs"
	"github.com/pulumi/pulumi/pkg/v2/codegen/python"
	"github.com/pulumi/pulumi/pkg/v2/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v2/resource/stack"
	"github.com/pulumi/pulumi/pkg/v2/x/importer"
	"github.com/pulumi/pulumi/sdk/v2/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v2/go/common/diag/colors"
	"github.com/pulumi/pulumi/sdk/v2/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v2/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v2/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v2/go/common/util/cmdutil"
)

func newImportCmd() *cobra.Command {
	var stackName string
	cmd := &cobra.Command{
		Use:   "import [type] [name] [id]",
		Args:  cmdutil.ExactArgs(3),
		Short: "Import a resource into an existing stack",
		Long: "Import a resource into an existing stack.\n" +
			"\n" +
			"A resource that is not managed by Pulumi can be imported into a Pulumi stack\n" +
			"using this command. A definition for the resource will be printed to stdout\n" +
			"in the language used by the project associated with the stack; this definition\n" +
			"should be added to the Pulumi program. The resource is protected from deletion\n" +
			"by default",
		Run: cmdutil.RunFunc(func(cmd *cobra.Command, args []string) error {
			opts := display.Options{
				Color: cmdutil.GetGlobalColorization(),
			}

			// Fetch the project.
			project, _, err := readProject()
			if err != nil {
				return err
			}

			var programGenerator func(p *hcl2.Program) (map[string][]byte, hcl.Diagnostics, error)
			switch project.Runtime.Name() {
			case "nodejs":
				programGenerator = nodejs.GenerateProgram
			case "python":
				programGenerator = python.GenerateProgram
			default:
				return fmt.Errorf("cannot generate resource definitions for %v", project.Runtime.Name())
			}

			// Fetch the current stack.
			s, err := requireStack(stackName, false, opts, true /*setCurrent*/)
			if err != nil {
				return err
			}
			stackName := s.Ref().Name()

			sm, err := getStackSecretsManager(s)
			if err != nil {
				return errors.Wrap(err, "getting secrets manager")
			}

			cfg, err := getStackConfiguration(s, sm)
			if err != nil {
				return errors.Wrap(err, "getting stack configuration")
			}

			// Export the stack's deployment.
			deployment, err := s.ExportDeployment(context.Background())
			if err != nil {
				return err
			}
			snap, err := stack.DeserializeUntypedDeployment(deployment, stack.DefaultSecretsProvider)
			if err != nil {
				switch err {
				case stack.ErrDeploymentSchemaVersionTooOld:
					return fmt.Errorf("the stack '%s' is too old to be used by this version of the Pulumi CLI",
						stackName)
				case stack.ErrDeploymentSchemaVersionTooNew:
					return fmt.Errorf("the stack '%s' is newer than what this version of the Pulumi CLI understands. "+
						"Please update your version of the Pulumi CLI", stackName)
				}

				return errors.Wrap(err, "could not deserialize deployment")
			}
			stackResource, err := stack.GetRootStackResource(snap)
			if err != nil {
				return err
			}

			// Build a URN for the resource.
			urn := resource.NewURN(stackName, project.Name, "", tokens.Type(args[0]), tokens.QName(args[1]))

			// Create a plugin context and host.
			target := &deploy.Target{
				Name:      stackName,
				Config:    cfg.Config,
				Decrypter: cfg.Decrypter,
				Snapshot:  snap,
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			sink := diag.DefaultSink(os.Stdout, os.Stderr, diag.FormatOptions{Pwd: cwd, Color: colors.Always})
			ctx, err := plugin.NewContext(sink, sink, nil, target, cwd, nil, nil)
			if err != nil {
				return err
			}
			host := ctx.Host

			// Load the default provider.
			//
			// TODO: allow a provider to be specified on the command line or pull from the snapshot, as we can't
			// otherwise store to the state.
			pkg := urn.Type().Package()
			prov, err := host.Provider(pkg, nil)
			if err != nil {
				return err
			}
			pkgCfg, err := target.GetPackageConfig(pkg)
			if err != nil {
				return err
			}
			inputs := resource.PropertyMap{}
			for k, v := range pkgCfg {
				inputs[resource.PropertyKey(k.Name())] = resource.NewStringProperty(v)
			}
			if err = prov.Configure(inputs); err != nil {
				return err
			}

			// Grab the resource's ID.
			id := resource.ID(args[2])

			var initErrors []string
			read, _, err := prov.Read(urn, id, nil, nil)
			if err != nil {
				if initErr, isInitErr := err.(*plugin.InitError); isInitErr {
					initErrors = initErr.Reasons
				} else {
					return err
				}
			}
			if read.Outputs == nil {
				return errors.Errorf("resource '%v' does not exist", id)
			}
			if read.Inputs == nil {
				return errors.Errorf(
					"provider does not support importing resources; please try updating the '%v' plugin",
					urn.Type().Package())
			}

			state := resource.NewState(urn.Type(), urn, true, false, id, resource.PropertyMap{}, read.Outputs,
				stackResource.URN, true, false, nil, initErrors, "",
				nil, false, nil, nil, nil, "")

			// Magic up an old state so we can perform a diff.
			old := resource.NewState(state.Type, state.URN, state.Custom, false, state.ID, read.Inputs, read.Outputs,
				state.Parent, state.Protect, false, state.Dependencies, state.InitErrors, state.Provider,
				state.PropertyDependencies, false, nil, nil, &state.CustomTimeouts, state.ImportID)

			// Diff the user inputs against the provider inputs. If there are any differences, fail the import.
			diff, err := deploy.DiffResource(state.URN, state.ID, old.Inputs, old.Outputs, state.Inputs, prov, false, nil)
			if err != nil {
				return err
			}

			if diff.Changes != plugin.DiffNone {
				for path, pdiff := range diff.DetailedDiff {
					elements, err := resource.ParsePropertyPath(path)
					if err != nil {
						continue
					}

					source := old.Outputs
					if pdiff.InputDiff {
						source = old.Inputs
					}

					if old, ok := elements.Get(resource.NewObjectProperty(source)); ok {
						// Ignore failure here.
						elements.Add(resource.NewObjectProperty(state.Inputs), old)
					}
				}
			}

			if err := importer.GenerateLanguageDefinition(os.Stdout, host, func(w io.Writer, p *hcl2.Program) error {
				files, _, err := programGenerator(p)
				if err != nil {
					return err
				}

				var contents []byte
				for _, v := range files {
					contents = v
				}

				if _, err := w.Write(contents); err != nil {
					return err
				}
				return nil
			}, state); err != nil {
				return err
			}

			// TODO: save to stack

			return nil
		}),
	}

	cmd.PersistentFlags().StringVarP(
		&stackName, "stack", "s", "", "The name of the stack to operate on. Defaults to the current stack")

	return cmd
}
