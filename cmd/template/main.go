package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"golang.org/x/tools/imports"
)

// exec from project root
const templatesDir = "cmd/template/files"

type TemplateExecutionConfig struct {
	TemplateFile    string
	DestinationFile string
	Data            any
}

type TemplateData struct {
	Group               string
	GroupSuffix         string
	Version             string
	Kind                string
	KindLower           string
	Module              string
	RepoName            string
	WithExample         bool
	WithWorkloadCluster bool
	WithSecretWatcher   bool
}

const groupSuffix = "services.open-control-plane.io"

//nolint:gocyclo
func main() {
	group := flag.String("group", "foo", fmt.Sprintf("GVK group prefix (will always be suffixed with %s", groupSuffix))
	kind := flag.String("kind", "FooService", "GVK kind")
	withExample := flag.Bool("v", false, "Generate with sample code")
	withWorkloadCluster := flag.Bool("w", false, "Reconcile with workload cluster")
	withSecretWatcher := flag.Bool("s", false, "Generate secret watcher implementation")
	module := flag.String("module", "github.com/openmcp-project/service-provider-template", "Go module")
	flag.Parse()
	data := TemplateData{
		Group:               *group,
		GroupSuffix:         groupSuffix,
		Kind:                *kind,
		KindLower:           strings.ToLower(*kind),
		Module:              *module,
		RepoName:            filepath.Base(*module),
		WithExample:         *withExample,
		WithWorkloadCluster: *withWorkloadCluster,
		WithSecretWatcher:   *withSecretWatcher,
	}
	// directories
	workflowsDir := filepath.Join(".github", "workflows")
	apiDir := filepath.Join("api", "v1alpha1")
	crdDir := filepath.Join("api", "crds", "manifests")
	cmdDir := filepath.Join("cmd", data.RepoName)
	controllerDir := filepath.Join("internal", "controller")
	e2eDir := filepath.Join("test", "e2e")

	if cmdDir != "cmd/service-provider-template" {
		err := os.Rename("cmd/service-provider-template", cmdDir)
		if err != nil {
			log.Fatalf("failed to rename directory: %v", err)
		}
	}

	templateConfigs := []TemplateExecutionConfig{
		{
			TemplateFile:    "api_crd_providerconfig.yaml.tmpl",
			DestinationFile: filepath.Join(crdDir, fmt.Sprintf("%s.%s_providerconfigs.yaml", *group, groupSuffix)),
			Data:            data,
		},
		{
			TemplateFile:    "api_crd_serviceproviderapi.yaml.tmpl",
			DestinationFile: filepath.Join(crdDir, fmt.Sprintf("%s.%s_%ss.yaml", *group, groupSuffix, data.KindLower)),
			Data:            data,
		},
		{
			TemplateFile:    "api_groupversion_info.go.tmpl",
			DestinationFile: filepath.Join(apiDir, "groupversion_info.go"),
			Data:            data,
		},
		{
			TemplateFile:    "api_types.go.tmpl",
			DestinationFile: filepath.Join(apiDir, fmt.Sprintf("%s_types.go", data.KindLower)),
			Data:            data,
		},
		{
			TemplateFile:    "controller.go.tmpl",
			DestinationFile: filepath.Join(controllerDir, fmt.Sprintf("%s_controller.go", data.KindLower)),
			Data:            data,
		},
		{
			TemplateFile:    "main_test.go.tmpl",
			DestinationFile: filepath.Join(e2eDir, "main_test.go"),
			Data:            data,
		},
		{
			TemplateFile:    "main.go.tmpl",
			DestinationFile: filepath.Join(cmdDir, "main.go"),
			Data:            data,
		},
		{
			TemplateFile:    "Taskfile.yaml.tmpl",
			DestinationFile: "Taskfile.yaml",
			Data:            data,
		},
		{
			TemplateFile:    "test.go.tmpl",
			DestinationFile: filepath.Join(e2eDir, fmt.Sprintf("%s_test.go", data.KindLower)),
			Data:            data,
		},
		{
			TemplateFile:    "testdata_providerconfig.yaml.tmpl",
			DestinationFile: filepath.Join(e2eDir, "platform", "providerconfig.yaml"),
			Data:            data,
		},
		{
			TemplateFile:    "testdata_service.yaml.tmpl",
			DestinationFile: filepath.Join(e2eDir, "onboarding", fmt.Sprintf("%s.yaml", data.KindLower)),
			Data:            data,
		},
		{
			TemplateFile:    "workflow_publish.yaml.tmpl",
			DestinationFile: filepath.Join(workflowsDir, "publish.yaml"),
			Data:            nil,
		},
		{
			TemplateFile:    "workflow_release.yaml.tmpl",
			DestinationFile: filepath.Join(workflowsDir, "release.yaml"),
			Data:            nil,
		},
	}

	for _, cfg := range templateConfigs {
		execTemplate(cfg)
	}

	// rename module
	if err := exec.Command("go", "mod", "edit", "-module", *module).Run(); err != nil {
		log.Fatalf("go mod edit failed: %v", err)
	}
	// replace module in imports and remove redundant imports
	rootDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("could not determine current working directory: %v", err)
	}
	err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			if err := replaceImports(path, "github.com/openmcp-project/service-provider-template", *module); err != nil {
				return err
			}
			if err := fixImports(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("rename imports failed: %v", err)
	}
	// clean up repo (remove /cmd/template)
	err = os.RemoveAll("cmd/template")
	if err != nil {
		log.Fatalf("Error removing template directory: %v", err)
		return
	}
	fmt.Printf("Generated service-provider for %s/%s' in %s\n", data.Group, data.Kind, *module)
}

//nolint:gocritic
func execTemplate(cfg TemplateExecutionConfig) {
	tplPath := filepath.Join(templatesDir, cfg.TemplateFile)
	tpl, err := template.ParseFiles(tplPath)
	if err != nil {
		log.Fatalf("failed parsing template %s: %v", cfg.TemplateFile, err)
	}
	f, err := os.Create(cfg.DestinationFile)
	if err != nil {
		log.Fatalf("failed creating file %s: %v", cfg.DestinationFile, err)
	}
	log.Default().Println(cfg.DestinationFile)
	defer closeFile(cfg.DestinationFile, f)
	if err := tpl.Execute(f, cfg.Data); err != nil {
		log.Fatalf("failed executing template %s: %v", cfg.TemplateFile, err)
	}
}

func replaceImports(filename, oldRepo, newRepo string) error {
	input, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer closeFile(filename, input)
	var result []string
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.Replace(line, oldRepo, newRepo, 1)
		result = append(result, line)
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	// keep empty line
	result = append(result, "")
	return os.WriteFile(filename, []byte(strings.Join(result, "\n")), 0644)
}

func fixImports(filename string) error {
	data, err := imports.Process(filename, nil, nil)
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func closeFile(filename string, c io.Closer) {
	if err := c.Close(); err != nil {
		log.Fatalf("please reset/checkout %s and try again: %v", filename, err)
	}
}
