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
	"strconv"
	"strings"

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
	ProjectId    int            `json:"project_id"`
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

	pflag.BoolP("yarn", "r", false, "Install packages using yarn")
	pflag.BoolP("composer", "c", false, "Install packages using composer")
	pflag.BoolP("ddev-composer", "e", false, "Install packages using composer in DDEV container")
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

	// Make sure some default folders are created
	osUserCacheDir, err := os.UserCacheDir()
	if err != nil {
		panic(fmt.Errorf("Can't find a user cache dir, is your $HOME set?: %w", err))
	}

	err = os.MkdirAll(path.Join(osUserCacheDir, "gitlab-review", "yarn"), 0750)
	if err != nil {
		panic(fmt.Errorf("Can't create cache dir: %w", err))
	}

	grCacheDir := path.Join(osUserCacheDir, "gitlab-review")

	//

	tailArgs := pflag.Args()
	if len(tailArgs) == 0 {
		panic(fmt.Errorf("No PR defined. Usage: gitlab-review [...options] [pr]"))
	}
	mr := pflag.Args()[0]

	// Start fetching MR info from GitLab
	fmt.Println("Reviewing MR:", mr)
	mrInfoChan := make(chan GitLabMergeRequest)
	go fetchPRInfo(mr, mrInfoChan)

	// In the meantime we can start setup the environment
	workingDir := getWorkingDir()
	// Create a temporary directory for storing caches
	tempDir, err := os.MkdirTemp("", "gitlab-review-*")
	if err != nil {
		panic(fmt.Errorf("Could not create temp dir to backup node_modules: %w", err))
	}

	if viper.GetBool("yarn") {
		BackupNodeModules(workingDir, tempDir)
		defer RestoreNodeModules(workingDir, tempDir)
	}

	isComposer := viper.GetBool("composer") || viper.GetBool("ddev-composer")
	if isComposer {
		BackupVendor(workingDir, tempDir)
		defer RestoreVendor(workingDir, tempDir)
	}

	isRepoClean := checkIfRepoIsClean()
	initialBranch := strings.TrimSpace(runGitCommand("rev-parse", "--abbrev-ref", "HEAD"))

	if !isRepoClean {
		runGitCommand("stash", "--include-untracked")
		defer runGitCommand("stash", "pop")
	}

	mrInfo := <-mrInfoChan

	fmt.Println("Switching to branch", mrInfo.SourceBranch)
	runGitCommand("fetch")
	runGitCommand("switch", mrInfo.SourceBranch)
	// Make sure the branch itself is updated
	runGitCommand("pull")

	defer func() {
		fmt.Println("Switching back to", string(initialBranch))
		cmd := exec.Command("git", "switch", initialBranch)
		res, err := cmd.Output()
		if err != nil {
			fmt.Println("Could not switch back to previous branch, %w", err)
		}
		fmt.Println(string(res))
	}()

	if viper.GetBool("yarn") {
		cacheDir := YarnInstall(workingDir, grCacheDir, mrInfo.ProjectId)
		defer RestoreYarnCache(workingDir, cacheDir)
	}

	if isComposer {
		isDdev := viper.GetBool("ddev-composer")
		cacheDir := ComposerInstall(workingDir, grCacheDir, mrInfo.ProjectId, isDdev)
		defer RestoreVendor(workingDir, cacheDir)
	}

	openShell(mr, mrInfo.Pipeline.Status)
}

func getWorkingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(fmt.Errorf("Could not get current working directory: %w", err))
	}

	return dir
}

func runGitCommand(arg ...string) string {
	cmd := exec.Command("git", arg...)
	res, err := cmd.Output()
	if err != nil {
		fmt.Println(string(res))
		panic(fmt.Errorf("Failed running git %s: %w", arg[0], err))
	}

	return string(res)
}

func checkIfRepoIsClean() bool {
	res := runGitCommand("status", "-s")

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
	color := 103 // yellow
	if status == "success" {
		color = 102 // green
	}
	ps1 := fmt.Sprintf(`PS1=\[\e[97;101;1m\] <Reviewing MR-%s> \[\e[0;30;%dm\] Pipeline %s \[\e[0m\] \w %% `, mr, color, status)
	cmd.Env = append(cmd.Env, ps1)
	_ = cmd.Run()
}
