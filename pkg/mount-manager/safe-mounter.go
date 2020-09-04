/*
Copyright 2018 The Kubernetes Authors.

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

package mountmanager

import (
	"k8s.io/utils/exec"
	"k8s.io/utils/mount"
)

func NewSafeMounter() *mount.SafeFormatAndMount {
	realMounter := mount.New("")
	realExec := exec.New()
	return &mount.SafeFormatAndMount{
		Interface: realMounter,
		Exec:      realExec,
	}

}
