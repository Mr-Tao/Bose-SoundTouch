package main

import (
	"fmt"
	"net"
	"net/http"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/stereopair"
	"github.com/urfave/cli/v2"
)

// getGroupStatus retrieves and prints the device's current stereo-pair state.
func getGroupStatus(c *cli.Context) error {
	clientConfig := GetClientConfig(c)
	PrintDeviceHeader("Getting group information", clientConfig.Host, clientConfig.Port)

	result, err := newGroupCoordinator(clientConfig).Inspect(clientConfig.Host)
	if err != nil {
		PrintError(fmt.Sprintf("Failed to get group: %v", err))
		printGroupResultDetails(result)

		return err
	}

	if result.Group == nil || result.Group.IsEmpty() {
		fmt.Println("Device is not in a stereo pair")

		return nil
	}

	printGroup(result.Group)

	return nil
}

// createGroup forms and verifies a stereo pair. LEFT is always the master.
func createGroup(c *cli.Context) error {
	leftIP := c.String("left")
	rightIP := c.String("right")
	name := c.String("name")

	if net.ParseIP(leftIP) == nil {
		PrintError(fmt.Sprintf("Invalid left IP address: %s", leftIP))

		return fmt.Errorf("invalid left IP: %s", leftIP)
	}

	if net.ParseIP(rightIP) == nil {
		PrintError(fmt.Sprintf("Invalid right IP address: %s", rightIP))

		return fmt.Errorf("invalid right IP: %s", rightIP)
	}

	clientConfig := GetClientConfig(c)
	PrintDeviceHeader(fmt.Sprintf("Creating stereo pair: LEFT=%s RIGHT=%s", leftIP, rightIP), leftIP, clientConfig.Port)

	result, err := newGroupCoordinator(clientConfig).Create(stereopair.CreateRequest{
		LeftIPAddress:  leftIP,
		RightIPAddress: rightIP,
		Name:           name,
	})
	if err != nil {
		PrintError(fmt.Sprintf("Failed to create stereo pair: %v", err))
		printGroupResultDetails(result)

		return err
	}

	PrintSuccess(fmt.Sprintf("Stereo pair created (id=%s)", result.Group.ID))
	printGroup(result.Group)

	return nil
}

// renameGroup updates and verifies the name on both stereo-pair members.
func renameGroup(c *cli.Context) error {
	clientConfig := GetClientConfig(c)
	newName := c.String("name")

	if newName == "" {
		PrintError("--name is required")

		return fmt.Errorf("name is required")
	}

	PrintDeviceHeader(fmt.Sprintf("Renaming stereo pair to %q", newName), clientConfig.Host, clientConfig.Port)

	coordinator := newGroupCoordinator(clientConfig)

	current, err := coordinator.Inspect(clientConfig.Host)
	if err != nil {
		PrintError(fmt.Sprintf("Failed to inspect stereo pair before rename: %v", err))
		printGroupResultDetails(current)

		return err
	}

	if current.Group == nil || current.Group.IsEmpty() {
		return fmt.Errorf("device is not in a stereo pair")
	}

	result, err := coordinator.Rename(stereopair.RenameRequest{
		MemberIPAddress: clientConfig.Host,
		ExpectedGroupID: current.Group.ID,
		Name:            newName,
	})
	if err != nil {
		PrintError(fmt.Sprintf("Failed to rename group: %v", err))
		printGroupResultDetails(result)

		return err
	}

	PrintSuccess(fmt.Sprintf("Stereo pair renamed to %q", result.Group.Name))
	printGroup(result.Group)

	return nil
}

// removeGroup dissolves and verifies the stereo pair on every member.
func removeGroup(c *cli.Context) error {
	clientConfig := GetClientConfig(c)
	PrintDeviceHeader("Removing stereo pair", clientConfig.Host, clientConfig.Port)

	coordinator := newGroupCoordinator(clientConfig)

	current, err := coordinator.Inspect(clientConfig.Host)
	if err != nil {
		PrintWarning(fmt.Sprintf("Stereo pair is degraded before removal: %v", err))
		printGroupResultDetails(current)

		if current.Group == nil || current.Group.IsEmpty() {
			return err
		}
	}

	if current.Group == nil || current.Group.IsEmpty() {
		fmt.Println("Device is not in a stereo pair — nothing to remove")

		return nil
	}

	dissolveHost := dissolveRecoveryHost(current, clientConfig.Host)

	result, err := coordinator.Dissolve(stereopair.DissolveRequest{
		MemberIPAddress: dissolveHost,
		ExpectedGroupID: current.Group.ID,
		ExpectedGroup:   current.Group,
	})
	if err != nil {
		PrintError(fmt.Sprintf("Failed to remove stereo pair: %v", err))
		printGroupResultDetails(result)

		return err
	}

	PrintSuccess("Stereo pair removed")

	return nil
}

func dissolveRecoveryHost(result stereopair.Result, fallback string) string {
	if result.Group == nil || result.Group.ID == "" {
		return fallback
	}

	for i := range result.Members {
		member := &result.Members[i]
		if member.Group != nil && member.Group.ID == result.Group.ID && net.ParseIP(member.IPAddress) != nil {
			return member.IPAddress
		}
	}

	return fallback
}

func newGroupCoordinator(config *ClientConfig) *stereopair.Coordinator {
	lifecycleConfig := *config
	if lifecycleConfig.Timeout < stereopair.RequestTimeout {
		lifecycleConfig.Timeout = stereopair.RequestTimeout
	}

	cleanupClient := &http.Client{Timeout: lifecycleConfig.Timeout}

	return stereopair.NewWithGenerationLifecyclePersistence(
		groupClientFactory(&lifecycleConfig),
		func(ref stereopair.GenerationRef) error {
			return stereopair.DeleteMargeGroupGeneration(cleanupClient, ref)
		},
		func(refs []stereopair.GenerationRef) error {
			return stereopair.EnsureMargeNoGroupGenerations(cleanupClient, refs)
		},
		func(ref stereopair.GenerationRef, name string) error {
			return stereopair.RenameMargeGroupGeneration(cleanupClient, ref, name)
		},
	)
}

// groupClientFactory addresses every member directly while retaining the
// effective CLI port and timeout.
func groupClientFactory(config *ClientConfig) stereopair.ClientFactory {
	baseConfig := *config

	return func(ipAddress string) (stereopair.Client, error) {
		memberConfig := baseConfig
		memberConfig.Host = ipAddress

		return CreateSoundTouchClient(&memberConfig)
	}
}

func printGroupResultDetails(result stereopair.Result) {
	if result.Status == stereopair.StatusDegraded {
		PrintWarning(fmt.Sprintf("Stereo-pair %s result is degraded", result.Operation))
	}

	for i := range result.Members {
		member := &result.Members[i]
		label := groupMemberLabel(i, member)

		if member.PreflightError != nil {
			PrintError(fmt.Sprintf("%s preflight failed: %v", label, member.PreflightError))
		}

		if member.MutationError != nil {
			PrintError(fmt.Sprintf("%s mutation failed: %v", label, member.MutationError))
		}

		if member.VerificationError != nil {
			PrintError(fmt.Sprintf("%s verification failed: %v", label, member.VerificationError))
		}

		if member.CompensationError != nil {
			PrintError(fmt.Sprintf("%s cleanup failed: %v", label, member.CompensationError))
		} else if member.CompensationAttempted && !member.CompensationVerified {
			PrintWarning(fmt.Sprintf("%s cleanup could not be verified", label))
		}
	}

	if result.CompensationAttempted {
		if result.CompensationComplete {
			PrintWarning("Partial stereo-pair state was cleaned up and verified")
		} else {
			PrintError("Partial stereo-pair state cleanup is incomplete")
		}
	}

	if result.PersistenceError != nil {
		PrintError(fmt.Sprintf("Persistent group generation update failed: %v", result.PersistenceError))
	}
}

func groupMemberLabel(index int, member *stereopair.MemberResult) string {
	if member.IPAddress != "" && member.DeviceID != "" {
		return fmt.Sprintf("%s (%s)", member.IPAddress, member.DeviceID)
	}

	if member.IPAddress != "" {
		return member.IPAddress
	}

	if member.DeviceID != "" {
		return member.DeviceID
	}

	return fmt.Sprintf("member %d", index+1)
}

func printGroup(g *models.Group) {
	fmt.Println("Stereo Pair Configuration:")
	fmt.Printf("  ID:        %s\n", g.ID)
	fmt.Printf("  Name:      %s\n", g.Name)
	fmt.Printf("  Master:    %s\n", g.MasterDeviceID)

	if g.Status != "" {
		fmt.Printf("  Status:    %s\n", g.Status)
	}

	for _, r := range g.Roles.Roles {
		fmt.Printf("  %-5s     %s", r.Role, r.DeviceID)

		if r.IPAddress != "" {
			fmt.Printf(" (IP: %s)", r.IPAddress)
		}

		fmt.Println()
	}
}
