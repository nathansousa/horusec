// Copyright 2020 ZUP IT SERVICOS EM TECNOLOGIA E INOVACAO SA
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

package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ZupIT/horusec-devkit/pkg/utils/env"
	"github.com/ZupIT/horusec-devkit/pkg/utils/logger"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerTypesFilters "github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/google/uuid"
	specs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/ZupIT/horusec/config"
	"github.com/ZupIT/horusec/internal/entities/docker"
	"github.com/ZupIT/horusec/internal/enums/images"
	"github.com/ZupIT/horusec/internal/helpers/messages"
)

// ErrImageTagCmdRequired occurs when an docker image or docker command is empty to start analysis.
var ErrImageTagCmdRequired = errors.New("image or cmd is empty")

// Docker is the interface that abstract the Docker API.
type Docker interface {
	CreateLanguageAnalysisContainer(data *docker.AnalysisData) (containerOutPut string, err error)
	PullImage(imageWithTagAndRegistry string) error
	DeleteContainersFromAPI()
}

// Client is the API client that performs all operations against a docker server.
//
// Note that these comments was copied and paste from the docker client implementation
// from github.com/docker/docker/client.
type Client interface {
	// ContainerCreate creates a new container based in the given configuration.
	// It can be associated with a name, but it's not mandatory.
	ContainerCreate(
		ctx context.Context,
		cfg *container.Config,
		hostCfg *container.HostConfig,
		netCfg *network.NetworkingConfig,
		plataform *specs.Platform,
		name string,
	) (container.ContainerCreateCreatedBody, error)

	// ContainerStart sends a request to the docker daemon to start a container.
	ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error

	// ContainerList returns the list of containers in the docker host.
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)

	// ContainerWait waits until the specified container is in a certain state
	// indicated by the given condition, either "not-running" (default),
	// "next-exit", or "removed".
	ContainerWait(
		ctx context.Context, containerID string, condition container.WaitCondition,
	) (<-chan container.ContainerWaitOKBody, <-chan error)

	// ContainerLogs returns the logs generated by a container in an io.ReadCloser.
	// It's up to the caller to close the stream.
	ContainerLogs(ctx context.Context, containerID string, options types.ContainerLogsOptions) (io.ReadCloser, error)

	// ContainerRemove kills and removes a container from the docker host.
	ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error

	// ImageList returns a list of images in the docker host.
	ImageList(ctx context.Context, options types.ImageListOptions) ([]types.ImageSummary, error)

	// ImagePull requests the docker host to pull an image from a remote registry.
	ImagePull(ctx context.Context, ref string, options types.ImagePullOptions) (io.ReadCloser, error)

	// Ping pings the server and returns the value of the "Docker-Experimental",
	// "Builder-Version", "OS-Type" & "API-Version" headers. It attempts to use
	// a HEAD request on the endpoint, but falls back to GET if HEAD is not supported
	// by the daemon.
	Ping(ctx context.Context) (types.Ping, error)
}

type API struct {
	mutex                  *sync.RWMutex
	ctx                    context.Context
	dockerClient           Client
	config                 *config.Config
	analysisID             uuid.UUID
	pathDestinyInContainer string
}

func New(client Client, cfg *config.Config, analysisID uuid.UUID) *API {
	return &API{
		mutex:                  new(sync.RWMutex),
		ctx:                    context.Background(),
		dockerClient:           client,
		config:                 cfg,
		analysisID:             analysisID,
		pathDestinyInContainer: "/src",
	}
}

func (d *API) CreateLanguageAnalysisContainer(data *docker.AnalysisData) (containerOutPut string, err error) {
	if data.IsInvalid() {
		return "", ErrImageTagCmdRequired
	}

	return d.logStatusAndExecuteCRDContainer(data.GetCustomOrDefaultImage(), d.replaceCMDAnalysisID(data.CMD))
}

// PullImage check if an image already exists on cache, if its not, pull from registry.
//
// nolint:funlen
func (d *API) PullImage(imageWithTagAndRegistry string) error {
	if d.config.DisableDocker {
		return nil
	}

	imageNotExist, err := d.checkIfImageNotExists(imageWithTagAndRegistry)
	if err != nil {
		logger.LogError(fmt.Sprintf("%s -> %s",
			messages.MsgErrorFailedToPullImage, imageWithTagAndRegistry), err)
		return err
	} else if imageNotExist {
		logger.LogDebugWithLevel(fmt.Sprintf(messages.MsgDebugDockerImageDoesNotExists, imageWithTagAndRegistry))
		err = d.downloadImage(imageWithTagAndRegistry)
		logger.LogError(fmt.Sprintf("%s -> %s", messages.MsgErrorFailedToPullImage, imageWithTagAndRegistry), err)
		return err
	}

	return nil
}

func (d *API) downloadImage(imageWithTagAndRegistry string) error {
	d.loggerAPIStatus(messages.MsgDebugDockerAPIPullNewImage, imageWithTagAndRegistry)
	reader, err := d.dockerClient.ImagePull(d.ctx, imageWithTagAndRegistry, d.setPullOptions())
	if err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerPullImage, err)
		return err
	}

	return d.readPullReader(imageWithTagAndRegistry, reader)
}

func (d *API) setPullOptions() types.ImagePullOptions {
	authConfig := types.AuthConfig{
		Username:      env.GetEnvOrDefault("HORUSEC_CLI_REGISTRY_USERNAME", ""),
		Password:      env.GetEnvOrDefault("HORUSEC_CLI_REGISTRY_PASSWORD", ""),
		ServerAddress: env.GetEnvOrDefault("HORUSEC_CLI_REGISTRY_ADDRESS", ""),
	}

	if authConfig.Username != "" && authConfig.Password != "" {
		encodedAuthConfig, _ := json.Marshal(authConfig)
		return types.ImagePullOptions{RegistryAuth: base64.URLEncoding.EncodeToString(encodedAuthConfig)}
	}

	return types.ImagePullOptions{}
}

func (d *API) readPullReader(imageWithTagAndRegistry string, reader io.ReadCloser) error {
	readResult, err := io.ReadAll(reader)
	if err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerPullImage, err)
		logger.LogDebugWithLevel(string(readResult))
		return err
	}

	d.loggerAPIStatus(messages.MsgDebugDockerAPIDownloadWithSuccess, imageWithTagAndRegistry)
	return nil
}

// checkIfImageNotExists return true if image does not exists on cache, otherwise false.
func (d *API) checkIfImageNotExists(imageWithTagAndRegistry string) (bool, error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	args := dockerTypesFilters.NewArgs()
	args.Add("reference", d.removeRegistry(imageWithTagAndRegistry))
	options := types.ImageListOptions{Filters: args}

	result, err := d.dockerClient.ImageList(d.ctx, options)
	if err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerListImages, err)
		return false, err
	}

	return len(result) == 0, nil
}

func (d *API) replaceCMDAnalysisID(cmd string) string {
	return strings.ReplaceAll(cmd, "ANALYSISID", d.analysisID.String())
}

func (d *API) logStatusAndExecuteCRDContainer(imageNameWithTag, cmd string) (containerOutput string, err error) {
	containerOutput, err = d.executeCRDContainer(imageNameWithTag, cmd)
	if err != nil {
		d.loggerAPIStatus(messages.MsgDebugDockerAPIFinishedError, imageNameWithTag)
		return "", err
	}

	d.loggerAPIStatus(messages.MsgDebugDockerAPIFinishedSuccess, imageNameWithTag)
	return containerOutput, nil
}

// nolint:funlen
func (d *API) executeCRDContainer(imageNameWithTag, cmd string) (containerOutput string, err error) {
	containerID, err := d.createContainer(imageNameWithTag, cmd)
	if err != nil {
		return "", err
	}

	d.loggerAPIStatusWithContainerID(messages.MsgDebugDockerAPIContainerWait, imageNameWithTag, containerID)
	containerOutput, err = d.readContainer(containerID)
	if err != nil {
		return "", err
	}

	d.loggerAPIStatus(messages.MsgDebugDockerAPIContainerRead, imageNameWithTag)

	d.removeContainer(containerID)

	return containerOutput, nil
}

func (d *API) removeContainer(containerID string) {
	err := d.dockerClient.ContainerRemove(d.ctx, containerID, types.ContainerRemoveOptions{
		Force: true,
	})
	logger.LogErrorWithLevel(messages.MsgErrorDockerRemoveContainer, err)
}

func (d *API) createContainer(imageNameWithTag, cmd string) (string, error) {
	cfg, host := d.getContainerAndHostConfig(imageNameWithTag, cmd)

	response, err := d.dockerClient.ContainerCreate(d.ctx, cfg, host, nil, nil, d.getImageID())
	if err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerCreateContainer, err)
		return "", err
	}

	if err = d.dockerClient.ContainerStart(d.ctx, response.ID, types.ContainerStartOptions{}); err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerStartContainer, err)
		return "", err
	}

	d.loggerAPIStatusWithContainerID(messages.MsgDebugDockerAPIContainerCreated, imageNameWithTag, response.ID)
	return response.ID, nil
}

func (d *API) getImageID() string {
	return fmt.Sprintf("%s-%s", d.analysisID.String(), uuid.New().String())
}

// nolint: funlen
func (d *API) readContainer(containerID string) (string, error) {
	chanContainerStatus, _ := d.dockerClient.ContainerWait(d.ctx, containerID, "")

	if containerWaitStatus := <-chanContainerStatus; containerWaitStatus.Error != nil {
		message := fmt.Sprintf(
			"Error on wait container %s: %s | Exited with status %s",
			containerID,
			containerWaitStatus.Error.Message,
			strconv.Itoa(int(containerWaitStatus.StatusCode)),
		)
		return "", errors.New(message)
	}

	containerOutput, err := d.dockerClient.ContainerLogs(
		d.ctx, containerID, types.ContainerLogsOptions{
			ShowStdout: true,
		},
	)
	if err != nil {
		return "", err
	}

	return d.readOutputAsString(containerOutput)
}

func (d *API) readOutputAsString(output io.Reader) (string, error) {
	b, err := io.ReadAll(output)
	if err != nil {
		return "", err
	}

	return string(b), err
}

func (d *API) getContainerAndHostConfig(imageNameWithTag, cmd string) (*container.Config, *container.HostConfig) {
	cfg := d.getContainerConfig(imageNameWithTag, cmd)

	return cfg, d.getContainerHostConfig()
}

func (d *API) getContainerConfig(imageNameWithTag, cmd string) *container.Config {
	return &container.Config{
		Image: imageNameWithTag,
		Tty:   true,
		Cmd:   []string{"/bin/sh", "-c", fmt.Sprintf(`cd %s && %s`, d.pathDestinyInContainer, cmd)},
		Env:   []string{fmt.Sprintf("GITHUB_TOKEN=%s", os.Getenv("GITHUB_TOKEN"))},
	}
}

func (d *API) getContainerHostConfig() *container.HostConfig {
	return &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: d.getSourceFolder(),
				Target: d.pathDestinyInContainer,
				BindOptions: &mount.BindOptions{
					Propagation: mount.PropagationPrivate,
				},
			},
		},
	}
}

func (d *API) loggerAPIStatus(message, imageNameWithTag string) {
	logger.LogDebugWithLevel(
		message,
		map[string]interface{}{
			"image":      imageNameWithTag,
			"analysisId": d.analysisID.String(),
		},
	)
}

func (d *API) loggerAPIStatusWithContainerID(message, imageNameWithTag, containerID string) {
	logger.LogDebugWithLevel(
		message,
		map[string]interface{}{
			"image":       imageNameWithTag,
			"containerId": containerID,
			"analysisId":  d.analysisID.String(),
		},
	)
}

func (d *API) DeleteContainersFromAPI() {
	containers, err := d.listContainersByAnalysisID()
	if err != nil {
		logger.LogErrorWithLevel(messages.MsgErrorDockerListAllContainers, err)
		return
	}

	for index := range containers {
		err = d.dockerClient.ContainerRemove(d.ctx, containers[index].ID,
			types.ContainerRemoveOptions{Force: true})

		logger.LogErrorWithLevel(messages.MsgErrorDockerRemoveContainer, err)
	}
}

func (d *API) getSourceFolder() (path string) {
	if d.config.ContainerBindProjectPath != "" {
		path = filepath.Join(d.config.ContainerBindProjectPath, ".horusec", d.analysisID.String())
	} else {
		path = filepath.Join(d.config.ProjectPath, ".horusec", d.analysisID.String())
	}

	separator := path[1:2]
	if separator == ":" {
		return d.getSourceFolderFromWindows(path)
	}
	return path
}

func (d *API) listContainersByAnalysisID() ([]types.Container, error) {
	args := dockerTypesFilters.NewArgs()
	args.Add("name", d.analysisID.String())

	return d.dockerClient.ContainerList(d.ctx, types.ContainerListOptions{
		All:     true,
		Filters: args,
	})
}

func (d *API) getSourceFolderFromWindows(path string) string {
	// C:/Users/usr/Documents/Horusec/charlescd/.horusec/ID
	partitionLower := strings.ToLower(path[0:1])
	pathSplit := strings.Split(path, ":")
	pathSplit[0] = partitionLower
	path = strings.Join(pathSplit, ":")
	// c:/Users/usr/Documents/Horusec/project/.horusec/ID
	path = strings.ReplaceAll(path, ":", "")
	// c/Users/usr/Documents/Horusec/project/.horusec/ID
	path = "/" + path
	// /c/Users/usr/Documents/Horusec/project/.horusec/ID
	path = strings.ReplaceAll(path, "\\", "/")
	// /c/Users/usr/Documents/Horusec/project/.horusec/ID
	path = strings.ReplaceAll(path, "/", "//")
	// //c//Users//usr//Documents//Horusec//project//.horusec//ID
	return path
}

func (d *API) removeRegistry(imageWithTagAndRegistry string) string {
	return strings.ReplaceAll(imageWithTagAndRegistry, images.DefaultRegistry+"/", "")
}
