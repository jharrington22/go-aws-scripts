# go-aws-scripts
Collection of Go scripts for AWS actions

#### aws_modify_ebs_volume.go
Script to migrate EBS volumes on an OpenShift cluster to GP3

The script requires manual editing to add credentials and cluster name to modify volumes (requires ec2 instances to have the "Name" tag)

Script is called with no arguments `go run aws_modify_ebs_volumes.go`
