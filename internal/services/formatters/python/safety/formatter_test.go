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

package safety

import (
	"errors"
	"testing"
	"time"

	entitiesAnalysis "github.com/ZupIT/horusec-devkit/pkg/entities/analysis"
	enumHorusec "github.com/ZupIT/horusec-devkit/pkg/enums/analysis"
	"github.com/ZupIT/horusec-devkit/pkg/enums/tools"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	cliConfig "github.com/ZupIT/horusec/config"
	"github.com/ZupIT/horusec/internal/entities/toolsconfig"
	"github.com/ZupIT/horusec/internal/entities/workdir"
	"github.com/ZupIT/horusec/internal/services/formatters"
	"github.com/ZupIT/horusec/internal/utils/testutil"
)

func getAnalysis() *entitiesAnalysis.Analysis {
	return &entitiesAnalysis.Analysis{
		ID:                      uuid.New(),
		RepositoryID:            uuid.New(),
		WorkspaceID:             uuid.New(),
		Status:                  enumHorusec.Running,
		Errors:                  "",
		CreatedAt:               time.Now(),
		AnalysisVulnerabilities: []entitiesAnalysis.AnalysisVulnerabilities{},
	}
}

func TestNewFormatter(t *testing.T) {
	config := &cliConfig.Config{}
	config.WorkDir = &workdir.WorkDir{}

	service := formatters.NewFormatterService(nil, nil, config)

	assert.IsType(t, NewFormatter(service), &Formatter{})
}

func TestFormatter_StartSafety(t *testing.T) {
	t.Run("Should return error when start analysis", func(t *testing.T) {
		analysis := getAnalysis()

		config := &cliConfig.Config{}
		config.WorkDir = &workdir.WorkDir{}

		dockerAPIControllerMock := testutil.NewDockerMock()
		dockerAPIControllerMock.On("SetAnalysisID")
		dockerAPIControllerMock.On("CreateLanguageAnalysisContainer").Return("", errors.New("Error"))

		service := formatters.NewFormatterService(analysis, dockerAPIControllerMock, config)

		formatter := NewFormatter(service)

		assert.NotPanics(t, func() {
			formatter.StartAnalysis("")
		})
	})

	t.Run("Should execute analysis without error", func(t *testing.T) {
		analysis := getAnalysis()

		config := &cliConfig.Config{}
		config.WorkDir = &workdir.WorkDir{}
		output := `{"issues": [{"dependency": "jinja2","vulnerable_below": "2.7.2","installed_version": "2.7.2","description": "The default configuration for bccache.FileSystemBytecodeCache in Jinja2 before 2.7.2 does not properly create temporary files, which allows local users to gain privileges via a crafted .cache file with a name starting with __jinja2_ in /tmp.","id": "123"}]}`
		dockerAPIControllerMock := testutil.NewDockerMock()
		dockerAPIControllerMock.On("SetAnalysisID")
		dockerAPIControllerMock.On("CreateLanguageAnalysisContainer").Return(output, nil)

		service := formatters.NewFormatterService(analysis, dockerAPIControllerMock, config)

		formatter := NewFormatter(service)

		assert.NotPanics(t, func() {
			formatter.StartAnalysis("")
		})
	})

	t.Run("Should return nil when output is empty analysis", func(t *testing.T) {
		analysis := getAnalysis()

		config := &cliConfig.Config{}
		config.WorkDir = &workdir.WorkDir{}

		dockerAPIControllerMock := testutil.NewDockerMock()
		dockerAPIControllerMock.On("SetAnalysisID")
		dockerAPIControllerMock.On("CreateLanguageAnalysisContainer").Return("", nil)

		service := formatters.NewFormatterService(analysis, dockerAPIControllerMock, config)

		formatter := NewFormatter(service)

		assert.NotPanics(t, func() {
			formatter.StartAnalysis("")
		})
	})

	t.Run("Should return nil when output is wrong format analysis", func(t *testing.T) {
		analysis := getAnalysis()

		config := &cliConfig.Config{}
		config.WorkDir = &workdir.WorkDir{}

		dockerAPIControllerMock := testutil.NewDockerMock()
		dockerAPIControllerMock.On("SetAnalysisID")
		dockerAPIControllerMock.On("CreateLanguageAnalysisContainer").Return("some aleatory text", nil)

		service := formatters.NewFormatterService(analysis, dockerAPIControllerMock, config)

		formatter := NewFormatter(service)

		assert.NotPanics(t, func() {
			formatter.StartAnalysis("")
		})
	})
	t.Run("Should not execute tool because it's ignored", func(t *testing.T) {
		analysis := &entitiesAnalysis.Analysis{}
		dockerAPIControllerMock := testutil.NewDockerMock()
		config := &cliConfig.Config{}
		config.ToolsConfig = toolsconfig.ToolsConfig{
			tools.Safety: toolsconfig.Config{
				IsToIgnore: true,
			},
		}

		service := formatters.NewFormatterService(analysis, dockerAPIControllerMock, config)
		formatter := NewFormatter(service)

		formatter.StartAnalysis("")
	})
}
