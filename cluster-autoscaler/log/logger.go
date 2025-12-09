/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package log

import (
	"runtime"

	"github.com/sirupsen/logrus"
)

const (
	// SourceFieldName source
	SourceFieldName                 = "source"
	epochFieldName                  = "env_epoch"
	fileNameFieldName               = "fileName"
	lineNumberFieldName             = "lineNumber"
	methodNameFieldName             = "methodName"
	durationInMillisecondsFieldName = "durationInMilliseconds"
	resultFieldName                 = "result"
	errorDetailsFieldName           = "errorDetails"
	errorTypeFieldName              = "errorType"
	errorReasonFieldName            = "errorReason"
	propertiesFieldName             = "properties"
	startTimeFieldName              = "startTime"
	endTimeFieldName                = "endTime"
)

func withCallerInfo(logger *logrus.Entry) *logrus.Entry {
	_, file, line, _ := runtime.Caller(3)
	fields := make(map[string]interface{})
	fields[fileNameFieldName] = file
	fields[lineNumberFieldName] = line
	return logger.WithFields(fields)
}
