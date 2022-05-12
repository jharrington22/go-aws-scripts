package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sts"
)

func main() {
	region := "us-east-1"
	clusterName := "Cluster name here"
	ownedTag := fmt.Sprintf("kubernetes.io/cluster/%s*", clusterName)

	count := 0
	fmt.Println(region)
	s, err := session.NewSession(&aws.Config{
		Region: aws.String(region),
		// Use profile
		Credentials: credentials.NewSharedCredentials("", "jamesh"),
		// Use Env
		// Credentials: credentials.NewEnvCredentials(),
		// Use Static (token can be a blank string)
		// Credentials: credentials.NewStaticCredentials(id, secret, token string)

	})
	if err != nil {
		log.Fatal(err.Error())
	}
	GetAWSIdentity(sts.New(s))
	svc := ec2.New(s)

	input := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("tag:Name"),
				Values: []*string{
					aws.String(fmt.Sprintf("*%s*", clusterName)),
				},
			},
		},
	}

	result, err := svc.DescribeInstances(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			fmt.Println(err.Error())
		}
		return
	}

	var ebsBlockDeviceSpec []*ec2.EbsInstanceBlockDevice
	var volumeIds []*string
	var validOwnedTag = regexp.MustCompile(ownedTag)
	var ownedTagFound bool
	for _, r := range result.Reservations {
		for _, i := range r.Instances {
			fmt.Println(fmt.Sprintf("id: %s", *i.InstanceId))
			var nameTagFound bool
			for _, tag := range i.Tags {
				if *tag.Key == "Name" {
					nameTagFound = true
					fmt.Printf("Instance name: %s\n", *tag.Value)
				}
				if validOwnedTag.MatchString(*tag.Key) {
					ownedTagFound = true
					fmt.Printf("Owned Tag: %s\n", *tag.Key)
				}
			}
			if !ownedTagFound {
				fmt.Printf("Instance %s does not contain an owned flag\n", *i.InstanceId)
				break
			}
			if !nameTagFound {
				fmt.Printf("Instance has no tag \"Name\"\n")
			}
			fmt.Println(fmt.Sprintf("id: %s", *i.InstanceId))

			var newVolumeIds []string
			for _, bd := range i.BlockDeviceMappings {
				volumeIds = append(volumeIds, bd.Ebs.VolumeId)
				newVolumeIds = append(newVolumeIds, *bd.Ebs.VolumeId)
				ebsBlockDeviceSpec = append(ebsBlockDeviceSpec, bd.Ebs)
			}

			fmt.Printf("Found the following volumes: %s\n", strings.Join(newVolumeIds, ","))

			fmt.Println("Complete")
			count++
		}
	}

	if len(volumeIds) == 0 {
		fmt.Printf("No volumes found\n")
		os.Exit(1)
	}

	for _, id := range volumeIds {
		fmt.Printf("Describing volume: %s\n", *id)
	}
	ebsInput := &ec2.DescribeVolumesInput{
		VolumeIds: volumeIds,
	}

	ebsResult, err := svc.DescribeVolumes(ebsInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				fmt.Println(aerr.Error())
			}
		}
		fmt.Println(err.Error())
	}

	volumeType := "gp2"
	desiredVolumeType := "gp3"
	for _, v := range ebsResult.Volumes {
		switch vt := *v.VolumeType; vt {
		case desiredVolumeType:
			fmt.Printf("Volume %s is already type %s\n", *v.VolumeId, *v.VolumeType)
		case volumeType:
			fmt.Printf("Migrate volume %s which is type %s\n", *v.VolumeId, *v.VolumeType)
			if *v.Iops > 3000 {
				fmt.Printf("Volume %s IOPs greater than 3000: %d\n", *v.VolumeId, *v.Iops)
			}
			volumeInput := ec2.ModifyVolumeInput{
				VolumeId:   v.VolumeId,
				VolumeType: aws.String(desiredVolumeType),
			}
			modifyVolumeOutput, err := svc.ModifyVolume(&volumeInput)
			if err != nil {
				fmt.Printf("Failed to modify volume %s error: %s\n", *v.VolumeId, err)
			}

			fmt.Printf("Modifing volume %s: start time %s: state: %s\n", *modifyVolumeOutput.VolumeModification.VolumeId, *modifyVolumeOutput.VolumeModification.StartTime, *modifyVolumeOutput.VolumeModification.ModificationState)

			err = WaitUntilVolumeModified(svc, &ec2.DescribeVolumesModificationsInput{VolumeIds: []*string{v.VolumeId}})
			if err != nil {
				fmt.Printf("Failed to setup waiter: %s\n", err)
				os.Exit(1)
			}
		}

		status, err := VolumeModificationStatus(svc, *v.VolumeId)
		if err != nil {
			fmt.Printf("Failed to get volume status: %s", err)
		}
		if status != "completed" {
			fmt.Printf("Volume %s migration not complete status: %s\n", *v.VolumeId, status)
		}
		fmt.Printf("Volume id: %s modification %s\n", *v.VolumeId, status)
	}
}

func VolumeModificationStatus(client *ec2.EC2, volumeId string) (string, error) {
	output, err := client.DescribeVolumesModifications(&ec2.DescribeVolumesModificationsInput{VolumeIds: []*string{&volumeId}})
	if err != nil {
		return "", err
	}
	if len(output.VolumesModifications) == 0 {
		return "", fmt.Errorf("Volume %s not found", volumeId)
	}
	return *output.VolumesModifications[0].ModificationState, nil
}

func WaitUntilVolumeModified(client *ec2.EC2, input *ec2.DescribeVolumesModificationsInput) error {
	start := time.Now()
	delay := 15 * time.Second
	for {
		output, err := client.DescribeVolumesModifications(input)
		if err != nil {
			return err
		}
		elapsed := time.Now().Sub(start)
		fmt.Printf("Volume modification progressing %s\n", elapsed)
		if *output.VolumesModifications[0].ModificationState == "complete" {
			fmt.Printf("Modification completed after %v\n", elapsed)
			return nil
		}
		time.Sleep(delay)
	}
}

func GetAWSIdentity(client *sts.STS) {
	identity, err := client.GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		fmt.Printf("STS error: %s", err)
		os.Exit(1)
	}

	fmt.Printf("Account %s\n", *identity.Account)
}
