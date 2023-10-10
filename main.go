package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	// Make sure some default folders are created
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		panic(fmt.Errorf("Can't find a user cache dir, is your $HOME set?: %w", err))
	}

	err = os.MkdirAll(path.Join(userCacheDir, "gitlab-review", "yarn"), 0750)
	if err != nil {
		panic(fmt.Errorf("Can't create cache dir: %w", err))
	}

	//

	tailArgs := pflag.Args()
	if len(tailArgs) == 0 {
		panic(fmt.Errorf("No PR defined. Usage: gitlab-review [...options] [pr]"))
	}
	pr := pflag.Args()[0]

	fmt.Println("PR:", pr)
	mrInfoChan := make(chan GitLabMergeRequest)
	go fetchPRInfo(pr, mrInfoChan)

	// Meanwhile while the MR info is being fetched start preparing a clean repo
	ds, ib, td, cd := false, "", "", ""
	initialBranch := &ib
	didStash := &ds
	tempDir := &td
	yarnCacheDir := &cd

	// From now on we make changes to the working directory that might need to be restored
	defer func() {
		fmt.Println("Cleanup temporary changes")
		currentDir := getWorkingDir()

		fmt.Println("Restoring from", *tempDir)

		if len(*yarnCacheDir) > 0 {
			err = os.Rename(path.Join(getWorkingDir(), "node_modules"), *yarnCacheDir)
			if err != nil {
				log.Println("Could not save to cache %w", err)
			}
		}

		modulesBackupDir := path.Join(*tempDir, "node_modules")
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

		fmt.Println("Switching back to", string(*initialBranch))
		// runGitCommand("switch", string(*initialBranch))
		cmd := exec.Command("git", "switch", *initialBranch)
		res, err := cmd.Output()
		if err != nil {
			fmt.Println("Could not switch back to previous branch, %w", err)
		}
		fmt.Println(string(res))

		if *didStash {
			runGitCommand("stash", "pop")
		}

	}()
	*tempDir = backupNodeModules()

	isRepoClean := checkIfRepoIsClean()
	*initialBranch = strings.TrimSpace(runGitCommand("rev-parse", "--abbrev-ref", "HEAD"))

	if !isRepoClean {
		runGitCommand("stash", "--include-untracked")
		*didStash = true
	}

	mrInfo := <-mrInfoChan
	fmt.Println("Switching to branch", mrInfo.SourceBranch)
	runGitCommand("fetch")
	runGitCommand("switch", mrInfo.SourceBranch)
	// Make sure the branch itself is updated
	runGitCommand("pull")

	if viper.GetBool("yarn") {
		*yarnCacheDir = path.Join(userCacheDir, "gitlab-review", "yarn", strconv.Itoa(mrInfo.ProjectId))
		yarnInstall(*yarnCacheDir)
	}

	openShell(pr, mrInfo.Pipeline.Status)
}

func yarnInstall(cacheDir string) {
	os.MkdirAll(cacheDir, 0750)
	// Symlink has issues in some projects
	// err := os.Symlink(cacheDir, path.Join(getWorkingDir(), "node_modules"))
	err := os.Rename(cacheDir, path.Join(getWorkingDir(), "node_modules"))
	if err != nil {
		log.Println("Could not use cache %w", err)
	}

	cmd := exec.Command("yarn", "install", "--prefer-offline")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		log.Println("Could not install yarn dependencies")
	}

}

func backupNodeModules() string {
	dir, err := os.MkdirTemp("", "gitlab-review-*")
	if err != nil {
		panic(fmt.Errorf("Could not create temp dir to backup node_modules: %w", err))
	}

	fmt.Println("Moving node_modules to", dir)
	os.Rename(path.Join(getWorkingDir(), "node_modules"), path.Join(dir, "node_modules"))

	return dir
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
