package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	repo "github.com/fsamin/go-repo"

	"github.com/ovh/cds/cli"
	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/cdsclient"
	"github.com/ovh/cds/sdk/exportentities"
)

var workflowInitCmd = cli.Command{
	Name:  "init",
	Short: "Init a workflow",
	Long: `[WARNING] THIS IS AN EXPERIMENTAL FEATURE
Initialize a workflow from your current repository, this will create yml files and push them to CDS.

Documentation: https://ovh.github.io/cds/gettingstarted/firstworkflow/

`,
	OptionalArgs: []cli.Arg{
		{Name: _ProjectKey},
	},
	Flags: []cli.Flag{
		{
			Name:      "from-remote",
			ShortHand: "r",
			Usage:     "Initialize a workflow from your git origin",
			Type:      cli.FlagBool,
		},
	},
}

func interactiveChooseProject(gitRepo repo.Repo) (string, error) {
	projs, err := client.ProjectList(false, false)
	if err != nil {
		return "", err
	}

	var chosenProj *sdk.Project
	opts := make([]string, len(projs))
	for i := range projs {
		opts[i] = fmt.Sprintf("%s - %s", projs[i].Key, projs[i].Name)
	}
	selected := cli.MultiChoice("Choose the CDS project:", opts...)
	chosenProj = &projs[selected]

	if err := gitRepo.LocalConfigSet("cds", "project", chosenProj.Key); err != nil {
		return "", err
	}

	return chosenProj.Key, nil
}

func workflowInitRunFromRemote(c cli.Values) error {
	path := "."
	gitRepo, errRepo := repo.New(path)
	if errRepo != nil {
		return errRepo
	}

	var pkey = c.GetString(_ProjectKey)
	if pkey == "" {
		var err error
		pkey, err = interactiveChooseProject(gitRepo)
		if err != nil {
			return err
		}
	}

	repoName, _ := gitRepo.Name()
	if repoName == "" {
		return fmt.Errorf("unable to retrieve repository name")
	}

	fetchURL, _ := gitRepo.FetchURL()
	if fetchURL == "" {
		return fmt.Errorf("unable to retrieve origin URL")
	}

	fmt.Printf("Initializing workflow from %s (%v)...\n", cli.Magenta(repoName), cli.Magenta(fetchURL))

	ope, err := client.WorkflowAsCodeStart(pkey, fetchURL, sdk.RepositoryStrategy{})
	if err != nil {
		return fmt.Errorf("unable to perform operation: %v", err)
	}

	for ope.Status == sdk.OperationStatusPending || ope.Status == sdk.OperationStatusProcessing {
		ope, err = client.WorkflowAsCodeInfo(pkey, ope.UUID)
		if err != nil {
			return fmt.Errorf("unable to perform operation: %v", err)
		}
	}

	msgList, err := client.WorkflowAsCodePerform(pkey, ope.UUID)
	for _, msg := range msgList {
		fmt.Println("\t" + msg)
	}

	if err != nil {
		return fmt.Errorf("unable to perform operation: %v", err)
	}

	return nil
}

func workflowInitRun(c cli.Values) error {
	if c.GetBool("from-remote") {
		return workflowInitRunFromRemote(c)
	}

	path := "."
	gitRepo, errRepo := repo.New(path)
	if errRepo != nil {
		return errRepo
	}

	var pkey = c.GetString(_ProjectKey)
	if pkey == "" {
		var err error
		pkey, err = interactiveChooseProject(gitRepo)
		if err != nil {
			return err
		}
	}

	repoName, _ := gitRepo.Name()
	if repoName == "" {
		return fmt.Errorf("unable to retrieve repository name")
	}

	fullnames := strings.SplitN(repoName, "/", 2)
	name := fullnames[1]

	fetchURL, _ := gitRepo.FetchURL()
	if fetchURL == "" {
		return fmt.Errorf("unable to retrieve origin URL")
	}

	fmt.Printf("Initializing workflow from %s (%v)...\n", cli.Magenta(repoName), cli.Magenta(fetchURL))

	dotCDS := filepath.Join(path, ".cds")

	var shouldCreateWorkflowDir, shouldCreateWorkflowFiles, shouldCreateApplication, shouldCreatePipeline bool
	var existingApp *sdk.Application
	var existingPip *sdk.Pipeline
	var repoManagerName string

	if _, err := os.Stat(dotCDS); err != nil && os.IsNotExist(err) {
		shouldCreateWorkflowDir = true
	}

	if shouldCreateWorkflowDir {
		if err := os.MkdirAll(dotCDS, os.FileMode(0755)); err != nil {
			return err
		}
	}

	files, err := filepath.Glob(dotCDS + "/*.yml")
	if err != nil {
		return err
	}

	if len(files) == 0 {
		shouldCreateWorkflowFiles = true
	}

	if !shouldCreateWorkflowFiles {
		fmt.Println("Loading yaml files...")
		//TODO
		return fmt.Errorf("Not yet implemented: you have already .cds/ files, please use web UI to use them for now")
	}

	// Check if the project is linked to a repository
	proj, err := client.ProjectGet(pkey, func(r *http.Request) {
		q := r.URL.Query()
		q.Set("withKeys", "true")
		r.URL.RawQuery = q.Encode()
	})
	if err != nil {
		return fmt.Errorf("unable to get project: %v", err)
	}

	if len(proj.VCSServers) == 0 {
		//TODO ask to link the project
		return fmt.Errorf("your CDS project must be linked to a repositories manager to perform this operation")
	}

	// Ask the user to choose the repository
	repoManagerNames := make([]string, len(proj.VCSServers))
	for i, s := range proj.VCSServers {
		repoManagerNames[i] = s.Name
	}
	selected := cli.MultiChoice("Choose the repository manager:", repoManagerNames...)
	repoManagerName = proj.VCSServers[selected].Name

	// Get repositories from the repository manager
	repos, err := client.RepositoriesList(pkey, repoManagerName)
	if err != nil {
		return fmt.Errorf("unable to list repositories from %s: %v", repoManagerName, err)
	}

	// Check it the project with it's delegated oauth knows the current repo
	// Search the repo
	var repoFound bool
	for _, r := range repos {
		// r.Fullname = CDS/demo
		if strings.ToLower(r.Fullname) == repoName {
			repoName = r.Fullname
			repoFound = true
		}
	}
	if !repoFound {
		return fmt.Errorf("unable to find repository %s from %s: please check your credentials", repoName, repoManagerName)
	}

	// Try to find application or create a new one from the repo
	apps, err := client.ApplicationList(pkey)
	if err != nil {
		return fmt.Errorf("unable to list applications: %v", err)
	}

	for i, a := range apps {
		if a.RepositoryFullname == repoName {
			fmt.Printf("application %s/%s (%s) found in CDS\n", cli.Magenta(a.ProjectKey), cli.Magenta(a.Name), cli.Magenta(a.RepositoryFullname))
			existingApp = &apps[i]
			break
		} else if a.Name == name {
			fmt.Printf("application %s/%s found in CDS.\n", cli.Magenta(a.ProjectKey), cli.Magenta(a.Name))
			fmt.Println(cli.Red("But it's not linked to repository"), cli.Red(repoName))
			if !cli.AskForConfirmation(cli.Red("Do you want to overwrite it?")) {
				return fmt.Errorf("operation aborted")
			}
			shouldCreateApplication = true
			break
		}
	}

	if existingApp == nil {
		shouldCreateApplication = true
	}

	// Try to find pipeline or create a new pipeline
	pips, err := client.PipelineList(pkey)
	if err != nil {
		return fmt.Errorf("unable to list pipelines: %v", err)
	}
	if len(pips) == 0 {
		shouldCreatePipeline = true
	} else if !cli.AskForConfirmation("Do you want to reuse an existing pipeline?") {
		shouldCreatePipeline = true
	} else {
		pipelineNames := make([]string, len(pips))
		for i, p := range pips {
			pipelineNames[i] = p.Name
		}
		selected := cli.MultiChoice("Choose your pipeline:", pipelineNames...)
		existingPip = &pips[selected]
	}

	var pipName string
	if shouldCreatePipeline {
		fmt.Print("Enter your pipeline name: ")
		pipName = cli.ReadLine()

		regexp := sdk.NamePatternRegex
		if !regexp.MatchString(pipName) {
			return fmt.Errorf("Pipeline name '%s' do not respect pattern %s", pipName, sdk.NamePattern)
		}
	}

	if existingPip != nil {
		pipName = existingPip.Name
	}

	var appName = name
	if existingApp != nil {
		appName = existingApp.Name
	}

	// Crafting the workflow
	wkflw := exportentities.Workflow{
		Version:         exportentities.WorkflowVersion1,
		Name:            name,
		ApplicationName: appName,
		PipelineName:    pipName,
	}

	b, err := exportentities.Marshal(wkflw, exportentities.FormatYAML)
	if err != nil {
		return fmt.Errorf("Unable to write workflow file format: %v", err)
	}

	wFilePath := filepath.Join(dotCDS, name+".yml")
	if err := ioutil.WriteFile(wFilePath, b, os.FileMode(0644)); err != nil {
		return fmt.Errorf("Unable to write workflow file: %v", err)
	}

	fmt.Printf("File %s created\n", cli.Magenta(wFilePath))
	files = []string{wFilePath}

	// Crafting the application
	if shouldCreateApplication {
		connectionType := "ssh"
		if strings.HasPrefix(fetchURL, "https") {
			connectionType = "https"
		}

		app := exportentities.Application{
			Name:              appName,
			RepositoryName:    repoName,
			VCSServer:         repoManagerName,
			VCSConnectionType: connectionType,
			Keys:              map[string]exportentities.KeyValue{},
		}

		projectPGPKeys := make([]sdk.ProjectKey, 0, len(proj.Keys))
		projectSSHKeys := make([]sdk.ProjectKey, 0, len(proj.Keys))
		for i := range proj.Keys {
			switch proj.Keys[i].Type {
			case "pgp":
				projectPGPKeys = append(projectPGPKeys, proj.Keys[i])
			case "ssh":
				projectSSHKeys = append(projectSSHKeys, proj.Keys[i])
			}
		}

		// ask for pgp key, if not selected or no existing key create a new one.
		if len(projectPGPKeys) > 1 {
			opts := make([]string, len(projectPGPKeys)+1)
			opts[0] = "Use a new pgp key"
			for i := range projectPGPKeys {
				opts[i+1] = projectPGPKeys[i].Name
			}
			selected := cli.MultiChoice("Select a PGP key to use in application VCS strategy", opts...)
			if selected > 0 {
				app.VCSPGPKey = opts[selected]
			}
		} else if len(projectPGPKeys) == 1 {
			if cli.AskForConfirmation(fmt.Sprintf("Found one existing PGP key '%s' on project. Use it in application VCS strategy?", projectPGPKeys[0].Name)) {
				app.VCSPGPKey = projectPGPKeys[0].Name
			}
		}
		if app.VCSPGPKey == "" {
			app.VCSPGPKey = fmt.Sprintf("app-pgp-%s", repoManagerName)
			app.Keys[app.VCSPGPKey] = exportentities.KeyValue{Type: sdk.KeyTypePGP}
		}

		// ask for ssh key if connection type is ssh, if not selected or no existing key create a new one
		if connectionType == "ssh" {
			if len(projectSSHKeys) > 1 {
				opts := make([]string, len(projectSSHKeys)+1)
				opts[0] = "Use a new ssh key"
				for i := range projectSSHKeys {
					opts[i+1] = projectSSHKeys[i].Name
				}
				selected := cli.MultiChoice("Select a SSH key to use in application VCS strategy", opts...)
				if selected > 0 {
					app.VCSSSHKey = opts[selected]
				}
			} else if len(projectSSHKeys) == 1 {
				if cli.AskForConfirmation(fmt.Sprintf("Found one existing SSH key '%s' on project. Use it in application VCS strategy?", projectSSHKeys[0].Name)) {
					app.VCSSSHKey = projectSSHKeys[0].Name
				}
			}
			if app.VCSSSHKey == "" {
				app.VCSSSHKey = fmt.Sprintf("app-ssh-%s", repoManagerName)
				app.Keys[app.VCSSSHKey] = exportentities.KeyValue{Type: sdk.KeyTypeSSH}
			}
		}

		b, err := exportentities.Marshal(app, exportentities.FormatYAML)
		if err != nil {
			return fmt.Errorf("Unable to write application file format: %v", err)
		}

		appFilePath := filepath.Join(dotCDS, fmt.Sprintf(exportentities.PullApplicationName, appName))
		if err := ioutil.WriteFile(appFilePath, b, os.FileMode(0644)); err != nil {
			return fmt.Errorf("Unable to write application file: %v", err)
		}

		files = append(files, appFilePath)
		fmt.Printf("File %s created\n", cli.Magenta(appFilePath))
	}

	// Crafting the pipeline
	if shouldCreatePipeline {
		pip := exportentities.PipelineV1{
			Name:    pipName,
			Version: exportentities.PipelineVersion1,
			Jobs: []exportentities.Job{
				{
					Name: "First job",
					Steps: []exportentities.Step{
						{
							"checkout": "{{.cds.workspace}}",
						},
					},
				},
			},
		}

		b, err := exportentities.Marshal(pip, exportentities.FormatYAML)
		if err != nil {
			return fmt.Errorf("Unable to write pipeline file format: %v", err)
		}

		pipFilePath := filepath.Join(dotCDS, fmt.Sprintf(exportentities.PullPipelineName, pipName))
		if err := ioutil.WriteFile(pipFilePath, b, os.FileMode(0644)); err != nil {
			return fmt.Errorf("Unable to write application file: %v", err)
		}

		files = append(files, pipFilePath)
		fmt.Printf("File %s created\n", cli.Magenta(pipFilePath))
	}

	buf := new(bytes.Buffer)
	if err := workflowFilesToTarWriter(files, buf); err != nil {
		return err
	}

	fmt.Println("Pushing workflow to CDS...")
	mods := []cdsclient.RequestModifier{
		func(r *http.Request) {
			r.Header.Set(sdk.WorkflowAsCodeHeader, fetchURL)
		},
	}

	msgList, tr, err := client.WorkflowPush(pkey, buf, mods...)
	for _, msg := range msgList {
		fmt.Println("\t" + msg)
	}
	if err != nil {
		return err
	}

	if err := workflowTarReaderToFiles(dotCDS, tr, true, true); err != nil {
		return err
	}

	//Configure local git
	if err := gitRepo.LocalConfigSet("cds", "workflow", name); err != nil {
		return err
	}
	if err := gitRepo.LocalConfigSet("cds", "application", appName); err != nil {
		return err
	}

	fmt.Printf("Now you can run: ")
	fmt.Printf(cli.Magenta("git add %s/ && git commit -s -m \"chore: init CDS workflow files\"\n", dotCDS))

	keysList, err := client.ApplicationKeysList(pkey, appName)
	if err != nil {
		return err
	}

	if len(keysList) != 0 {
		fmt.Printf("You should consider add the following keys in %v \n", cli.Magenta(repoManagerName))
		for _, k := range keysList {
			fmt.Println(cli.Magenta(k.Type))
			fmt.Println(k.Public)
			fmt.Println()
		}
	}

	return nil
}
