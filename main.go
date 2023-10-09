package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

type GitLabUser struct {
	Id       int    `json:"id"`
	Username string `json:"username"`
	WebUrl   string `json:"web_url"`
}
type GitLabPipeline struct {
	Id     int    `json:"id"`
	Status string `json:"status"`
	WebUrl string `json:"web_url"`
}

type GitLabMergeRequest struct {
	Id           int            `json:"id"`
	Title        string         `json:"title"`
	TargetBranch string         `json:"target_branch"`
	SourceBranch string         `json:"source_branch"`
	Author       GitLabUser     `json:"author"`
	Pipeline     GitLabPipeline `json:"pipeline"`
}

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath("$XDG_CONFIG_HOME/gitlab-review")
	viper.AddConfigPath("$HOME/.config/gitlab-review")
	err := viper.ReadInConfig()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file is not found, it is not strictly required.
		} else {
			panic(fmt.Errorf("Could not load global config file: %w", err))
		}
	}

	viper.SetConfigName("gitlab-review")
	// Check the current directory and a few parent directories.
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	viper.AddConfigPath("../..")
	viper.AddConfigPath("../../..")

	pflag.Bool("yarn", false, "Install packages using yarn")
	pflag.Parse()

	viper.BindPFlags(pflag.CommandLine)

	err = viper.MergeInConfig()

	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// panic(fmt.Errorf("Could not load config file: %w", err))
			// Config file is not found, it is not strictly required.
		} else {
			panic(fmt.Errorf("Could not load config file: %w", err))
		}
	}

	fmt.Println("Will install using yarn?", viper.GetBool("yarn"))

	tailArgs := pflag.Args()
	if len(tailArgs) == 0 {
		panic(fmt.Errorf("No PR defined. Usage: gitlab-review [...options] [pr]"))
	}
	pr := pflag.Args()[0]

	fmt.Println("PR:", pr)
	mrInfoChan := make(chan GitLabMergeRequest)
	go fetchPRInfo(pr, mrInfoChan)

	// Meanwhile while the MR info is being fetched start preparing a clean repo
	tempDirChan := make(chan string)
	// From now on we make changes to the working directory that might need to be restored
	defer func() {
		fmt.Println("Cleanup temporary changes")
		currentDir := getWorkingDir()
		tempDir := <-tempDirChan

		modulesBackupDir := path.Join(tempDir, "node_modules")
		if _, err := os.Stat(modulesBackupDir); err != nil {
			if os.IsNotExist(err) {
				fmt.Println("node_modules backup not found, leaving current state intact.")
			} else {
				fmt.Println("Unkown error while restoring node_modules, you might need to reinstall them.")
			}
		} else {
			fmt.Println("Restoring node_modules from backup")
			os.RemoveAll(path.Join(currentDir, "node_modules"))
			os.Rename(modulesBackupDir, path.Join(currentDir, "node_modules"))

		}

	}()
	go backupNodeModules(tempDirChan)

	mrInfo := <-mrInfoChan
	fmt.Println("Switching to branch", mrInfo.SourceBranch)

	openShell(pr, mrInfo.Pipeline.Status)
}

func backupNodeModules(tempDir chan string) {
	dir, err := os.MkdirTemp("", "gitlab-review-*")
	if err != nil {
		panic(fmt.Errorf("Could not create temp dir to backup node_modules: %w", err))
	}

	fmt.Println("Moving node_modules to", dir)

	os.Rename(path.Join(getWorkingDir(), "node_modules"), path.Join(dir, "node_modules"))

	tempDir <- dir
}

func getWorkingDir() string {
	_, filename, _, _ := runtime.Caller(1)
	return path.Dir(filename)
}

func checkIfRepoIsClean() bool {
	cmd := exec.Command("git", "status", "-s")
	res, err := cmd.Output()
	if err != nil {
		panic(fmt.Errorf("Failed running git status: %w", err))
	}

	return len(res) == 0
}

func fetchPRInfo(pr string, mrInfo chan GitLabMergeRequest) {
	// Build PR API call
	instance := viper.GetString("instance")
	projectId := viper.GetInt("project_id")
	prEndpoint, err := url.JoinPath(instance, "api/v4/projects", strconv.Itoa(projectId), "merge_requests", pr)

	req, err := http.NewRequest("GET", prEndpoint, nil)
	if err != nil {
		panic(fmt.Errorf("Is the URL right? %s, %w", prEndpoint, err))
	}

	req.Header.Set("Authorization", "Bearer "+viper.GetString("token"))
	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		panic(fmt.Errorf("Failed to fetch MR info from GitLab instance: %w", err))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(fmt.Errorf("Failed to fetch MR info from GitLab instance: %w", err))
	}

	var m GitLabMergeRequest
	err = json.Unmarshal(body, &m)
	if err != nil {
		panic(fmt.Errorf("Failed to parse MR info: %w", err))
	}

	fmt.Println("Reviewing ", m.Title)

	mrInfo <- m

}

func openShell(mr string, status string) {
	cmd := exec.Command("bash")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	ps1 := fmt.Sprintf(`PS1=\[\e[97;101;1;5m\] <Reviewing MR-%s> \[\e[0;30;103m\] Pipeline %s \[\e[0m\] \w %% `, mr, status)
	cmd.Env = append(cmd.Env, ps1)
	_ = cmd.Run()
}
