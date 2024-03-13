/*
Copyright 2019 linkyard ag
Copyright cloudscale.ch
Copyright 2022 Akamai Technologies

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

// luks utilities from https://github.com/cloudscale-ch/csi-cloudscale/blob/master/driver/luks_util.go with some modifications for this driver

package linodebs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
)

type LuksContext struct {
	EncryptionEnabled bool
	EncryptionKey     string
	EncryptionCipher  string
	EncryptionKeySize string
	VolumeName        string
	VolumeLifecycle   VolumeLifecycle
}

const (
	// LuksEncryptedAttribute is used to pass the information if the volume should be
	// encrypted with luks to `NodeStageVolume`
	LuksEncryptedAttribute = driverName + "/luks-encrypted"

	// LuksCipherAttribute is used to pass the information about the luks encryption
	// cipher to `NodeStageVolume`
	LuksCipherAttribute = driverName + "/luks-cipher"

	// LuksKeySizeAttribute is used to pass the information about the luks key size
	// to `NodeStageVolume`
	LuksKeySizeAttribute = driverName + "/luks-key-size"

	// LuksKeyAttribute is the key of the luks key used in the map of secrets passed from the CO
	LuksKeyAttribute = "luksKey"
)

func (ctx *LuksContext) validate() error {
	if !ctx.EncryptionEnabled {
		return nil
	}

	var err error
	if ctx.VolumeName == "" {
		err = errors.Join(err, errors.New("no volume name provided"))
	}
	if ctx.EncryptionKey == "" {
		err = errors.Join(err, errors.New("no encryption key provided"))
	}
	if ctx.EncryptionCipher == "" {
		err = errors.Join(err, errors.New("no encryption cipher provided"))
	}
	if ctx.EncryptionKeySize == "" {
		err = errors.Join(err, errors.New("no encryption key size provided"))
	}

	return err
}

func getLuksContext(secrets map[string]string, context map[string]string, lifecycle VolumeLifecycle) LuksContext {
	if context[LuksEncryptedAttribute] != "true" {
		return LuksContext{
			EncryptionEnabled: false,
			VolumeLifecycle:   lifecycle,
		}
	}

	luksKey := secrets[LuksKeyAttribute]
	luksCipher := context[LuksCipherAttribute]
	luksKeySize := context[LuksKeySizeAttribute]
	volumeName := context[PublishInfoVolumeName]

	return LuksContext{
		EncryptionEnabled: true,
		EncryptionKey:     luksKey,
		EncryptionCipher:  luksCipher,
		EncryptionKeySize: luksKeySize,
		VolumeName:        volumeName,
		VolumeLifecycle:   lifecycle,
	}
}

func luksFormat(ctx LuksContext, source string) error {
	cryptsetupCmd, err := getCryptsetupCmd()
	if err != nil {
		return err
	}

	// initialize the luks partition
	cryptsetupArgs := []string{
		"-v",
		"--batch-mode",
		"--cipher", ctx.EncryptionCipher,
		"--key-size", ctx.EncryptionKeySize,
		"--key-file", "-",
		"luksFormat", source,
	}

	klog.V(4).Info("executing cryptsetup luksFormat command ", cryptsetupArgs)

	cmd := exec.Command(cryptsetupCmd, cryptsetupArgs...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe for cryptsetup, got err: %w", err)
	}

	if _, err := io.WriteString(stdin, ctx.EncryptionKey); err != nil {
		return fmt.Errorf("failed to write to stdin pipe for cryptsetup, got err: %w, closing pipe: %w", err, stdin.Close())
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("failed to close stdin pipe, got err: %w", err)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cryptsetup luksFormat failed: %w cmd: '%s %s' output: %q",
			err, cryptsetupCmd, strings.Join(cryptsetupArgs, " "), string(out))
	}

	// open the luks partition
	klog.V(4).Info("luksOpen ", source)
	err = luksOpen(ctx, source)
	if err != nil {
		return fmt.Errorf("cryptsetup luksOpen failed: %w cmd: '%s %s' output: %q",
			err, cryptsetupCmd, strings.Join(cryptsetupArgs, " "), string(out))
	}

	defer func() {
		if e := luksClose(ctx.VolumeName); e != nil {
			klog.Errorf("cannot close luks device: %s", e.Error())
		}
	}()

	klog.V(4).Info("The LUKS volume name is ", ctx.VolumeName)

	return nil
}

// prepares a luks-encrypted volume for mounting and returns the path of the mapped volume
func luksPrepareMount(ctx LuksContext, source string) (string, error) {
	if err := luksOpen(ctx, source); err != nil {
		return "", err
	}
	return "/dev/mapper/" + ctx.VolumeName, nil
}

func luksClose(volume string) error {
	cryptsetupCmd, err := getCryptsetupCmd()
	if err != nil {
		return err
	}
	cryptsetupArgs := []string{"--batch-mode", "close", volume}

	klog.V(4).Info("executing cryptsetup close command")

	out, err := exec.Command(cryptsetupCmd, cryptsetupArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("removing luks mapping failed: %w cmd: '%s %s' output: %q",
			err, cryptsetupCmd, strings.Join(cryptsetupArgs, " "), string(out))
	}
	return nil
}

func luksOpen(ctx LuksContext, volume string) error {
	// check if the luks volume is already open
	if _, err := os.Stat("/dev/mapper/" + ctx.VolumeName); !os.IsNotExist(err) {
		klog.V(4).Infof("luks volume is already open %s", volume)
		return nil
	}

	cryptsetupCmd, err := getCryptsetupCmd()
	if err != nil {
		return err
	}
	cryptsetupArgs := []string{
		"--batch-mode",
		"luksOpen",
		"--key-file", "-",
		volume, ctx.VolumeName,
	}
	klog.V(4).Info("executing cryptsetup luksOpen command")

	cmd := exec.Command(cryptsetupCmd, cryptsetupArgs...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe for cryptsetup, got err: %w", err)
	}

	if _, err := io.WriteString(stdin, ctx.EncryptionKey); err != nil {
		return fmt.Errorf("failed to write to stdin pipe for cryptsetup, got err: %w, closing pipe: %w", err, stdin.Close())
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("failed to close stdin pipe, got err: %w", err)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cryptsetup luksOpen failed: %w cmd: '%s %s' output: %q",
			err, cryptsetupCmd, strings.Join(cryptsetupArgs, " "), string(out))
	}
	return nil
}

// check is a given mapping under /dev/mapper is a luks volume
func isLuksMapping(volume string) (bool, string, error) {
	if strings.HasPrefix(volume, "/dev/mapper/") {
		mappingName := volume[len("/dev/mapper/"):]
		cryptsetupCmd, err := getCryptsetupCmd()
		if err != nil {
			return false, mappingName, err
		}
		cryptsetupArgs := []string{"status", mappingName}

		out, err := exec.Command(cryptsetupCmd, cryptsetupArgs...).CombinedOutput()
		if err != nil {
			return false, mappingName, nil
		}
		for _, statusLine := range strings.Split(string(out), "\n") {
			if strings.Contains(statusLine, "type:") {
				if strings.Contains(strings.ToLower(statusLine), "luks") {
					return true, mappingName, nil
				}
				return false, mappingName, nil
			}
		}

	}
	return false, "", nil
}

func getCryptsetupCmd() (string, error) {
	cryptsetupCmd := "cryptsetup"
	_, err := exec.LookPath(cryptsetupCmd)
	if err != nil {
		if err == exec.ErrNotFound {
			return "", fmt.Errorf("%q executable not found in $PATH", cryptsetupCmd)
		}
		return "", err
	}
	return cryptsetupCmd, nil
}

func blkidValid(source string) (bool, error) {
	if source == "" {
		return false, errors.New("invalid source")
	}

	blkidCmd := "blkid"
	_, err := exec.LookPath(blkidCmd)
	if err != nil {
		if err == exec.ErrNotFound {
			return false, fmt.Errorf("%q executable invalid", blkidCmd)
		}
		return false, err
	}

	blkidArgs := []string{source}

	exitCode := 0
	cmd := exec.Command(blkidCmd, blkidArgs...)
	err = cmd.Run()
	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			return false, fmt.Errorf("checking blkdid failed: %w cmd: %q, args: %q", err, blkidCmd, blkidArgs)
		}
		ws := exitError.Sys().(syscall.WaitStatus)
		exitCode = ws.ExitStatus()
		if exitCode == 2 {
			return false, nil
		}
		return false, errors.New("checking blkdid failed")
	}

	return true, nil
}
