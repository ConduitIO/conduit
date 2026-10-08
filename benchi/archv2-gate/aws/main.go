// Copyright © 2026 Meroxa, Inc.
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

// Command aws runs the archv2-gate harness (../) on one dedicated EC2
// instance and leaves the results in a private S3 bucket.
//
// It is a separate module so the AWS SDK's EC2, IAM and SSM clients stay out
// of Conduit's go.mod.
//
//	go run . launch  -manifest m.json -account <id> -main-sha <sha> \
//	                 -fanout-pr 2946 -fanout-sha <sha> -harness-pr 2956 -harness-sha <sha>
//	go run . status  -manifest m.json
//	go run . cleanup -manifest m.json [-delete-bucket]
//
// launch creates, in order, and records each ID in the manifest as soon as it
// exists: a private results bucket (all public access blocked, SSE-S3,
// TLS-only), an IAM role and instance profile (AmazonSSMManagedInstanceCore
// plus PutObject on the run's prefix only), a security group with no ingress,
// and one on-demand instance on the latest Amazon Linux 2023 AMI in the default
// VPC. The instance schedules its own halt at -max-minutes before doing
// anything else, halts when the sessions finish, and is launched with
// InstanceInitiatedShutdownBehavior=terminate, so either halt terminates it.
// No credential is placed in user data: uploads use the instance role.
//
// cleanup terminates the instance if it is still alive, then deletes the
// security group, instance profile and role, and with -delete-bucket empties
// and deletes the bucket.
package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json" //nolint:depguard // separate module; does not depend on Conduit
	"errors"        //nolint:depguard // separate module; cannot import Conduit cerrors
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

//go:embed userdata.sh.tmpl
var userDataTmpl string

const (
	amiParameter   = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
	ssmCorePolicy  = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
	tagPurpose     = "archv2-gate-early"
	ec2TrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

var (
	shaRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digitRe = regexp.MustCompile(`^[0-9]+$`)
)

// manifest records every resource launch created. It is rewritten after each
// creation so a failed launch still leaves a complete list for cleanup.
type manifest struct {
	RunID               string    `json:"run_id"`
	Region              string    `json:"region"`
	Account             string    `json:"account"`
	CreatedAt           time.Time `json:"created_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	InstanceType        string    `json:"instance_type,omitempty"`
	InstanceTypeNote    string    `json:"instance_type_note,omitempty"`
	AMI                 string    `json:"ami,omitempty"`
	VPC                 string    `json:"vpc,omitempty"`
	Subnet              string    `json:"subnet,omitempty"`
	Bucket              string    `json:"bucket,omitempty"`
	Prefix              string    `json:"prefix,omitempty"`
	RoleName            string    `json:"role_name,omitempty"`
	InlinePolicyName    string    `json:"inline_policy_name,omitempty"`
	InstanceProfileName string    `json:"instance_profile_name,omitempty"`
	SecurityGroupID     string    `json:"security_group_id,omitempty"`
	InstanceID          string    `json:"instance_id,omitempty"`
	VolumeIDs           []string  `json:"volume_ids,omitempty"`
	LaunchedAt          time.Time `json:"launched_at,omitzero"`
	MainSHA             string    `json:"main_sha"`
	FanoutPR            string    `json:"fanout_pr"`
	FanoutSHA           string    `json:"fanout_sha"`
	HarnessPR           string    `json:"harness_pr"`
	HarnessSHA          string    `json:"harness_sha"`
	Rounds              int       `json:"rounds"`
	MaxMinutes          int       `json:"max_minutes"`
	Deleted             []string  `json:"deleted,omitempty"`

	path string
}

func (m *manifest) save() error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, append(raw, '\n'), 0o600)
}

func loadManifest(path string) (*manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &manifest{path: path}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return m, nil
}

type clients struct {
	ec2 *ec2.Client
	iam *iam.Client
	s3  *s3.Client
	ssm *ssm.Client
	sts *sts.Client
}

func newClients(ctx context.Context, region string) (clients, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return clients{}, err
	}
	return clients{
		ec2: ec2.NewFromConfig(cfg),
		iam: iam.NewFromConfig(cfg),
		s3:  s3.NewFromConfig(cfg),
		ssm: ssm.NewFromConfig(cfg),
		sts: sts.NewFromConfig(cfg),
	}, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: aws launch|status|cleanup [flags]")
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "launch":
		err = launch(ctx, os.Args[2:])
	case "status":
		err = status(ctx, os.Args[2:])
	case "cleanup":
		err = cleanup(ctx, os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "archv2-gate/aws:", err)
		os.Exit(1)
	}
}

func launch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "path of the manifest JSON to write (required)")
	region := fs.String("region", "us-west-1", "AWS region")
	account := fs.String("account", "", "expected AWS account ID; launch refuses to run in any other (required)")
	instanceType := fs.String("instance-type", "c7i.4xlarge", "instance type")
	fallbackType := fs.String("fallback-instance-type", "c6i.4xlarge", "instance type used if -instance-type is not offered in the region")
	volumeGB := fs.Int("volume-gb", 80, "root gp3 volume size")
	volumeMBps := fs.Int("volume-throughput", 1000, "root gp3 throughput, MB/s; the sinks write tens of MB/s each")
	volumeIOPS := fs.Int("volume-iops", 4000, "root gp3 IOPS (1000 MB/s needs at least 4000)")
	maxMinutes := fs.Int("max-minutes", 360, "hard runtime cap; the instance halts (and terminates) after this")
	rounds := fs.Int("rounds", 5, "rounds per harness session")
	mainSHA := fs.String("main-sha", "", "main commit both v1 and v2 arms are built from (required)")
	fanoutPR := fs.String("fanout-pr", "", "PR number of the fan-out change (required)")
	fanoutSHA := fs.String("fanout-sha", "", "head commit of -fanout-pr (required)")
	harnessPR := fs.String("harness-pr", "", "PR number carrying the harness (required)")
	harnessSHA := fs.String("harness-sha", "", "commit of the harness to build (required)")
	goVersion := fs.String("go-version", "1.25.14", "Go toolchain for the harness build (the images build in Docker)")
	_ = fs.Parse(args)

	if *manifestPath == "" || *account == "" {
		return errors.New("-manifest and -account are required")
	}
	for name, v := range map[string]string{"main-sha": *mainSHA, "fanout-sha": *fanoutSHA, "harness-sha": *harnessSHA} {
		if !shaRe.MatchString(v) {
			return fmt.Errorf("-%s must be a full 40-character lowercase commit SHA, got %q", name, v)
		}
	}
	for name, v := range map[string]string{"fanout-pr": *fanoutPR, "harness-pr": *harnessPR} {
		if !digitRe.MatchString(v) {
			return fmt.Errorf("-%s must be a PR number, got %q", name, v)
		}
	}
	if _, err := os.Stat(*manifestPath); err == nil {
		return fmt.Errorf("manifest %s already exists; run cleanup on it or pick another path", *manifestPath)
	}

	c, err := newClients(ctx, *region)
	if err != nil {
		return err
	}
	id, err := c.sts.GetCallerIdentity(ctx, nil)
	if err != nil {
		return fmt.Errorf("get caller identity: %w", err)
	}
	if aws.ToString(id.Account) != *account {
		return fmt.Errorf("credentials are for account %s, not %s; refusing", aws.ToString(id.Account), *account)
	}

	now := time.Now().UTC()
	m := &manifest{
		RunID:      now.Format("20060102t150405z"),
		Region:     *region,
		Account:    *account,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Duration(*maxMinutes) * time.Minute),
		MainSHA:    *mainSHA,
		FanoutPR:   *fanoutPR,
		FanoutSHA:  *fanoutSHA,
		HarnessPR:  *harnessPR,
		HarnessSHA: *harnessSHA,
		Rounds:     *rounds,
		MaxMinutes: *maxMinutes,
		path:       *manifestPath,
	}
	if err := m.save(); err != nil {
		return err
	}

	rootDevice, err := placeInstance(ctx, c, m, *instanceType, *fallbackType)
	if err != nil {
		return err
	}
	tags := map[string]string{
		"Project":   "conduit",
		"Purpose":   tagPurpose,
		"Owner":     "devaris",
		"ExpiresAt": m.ExpiresAt.Format(time.RFC3339),
		"RunID":     m.RunID,
	}

	// Results bucket.
	m.Bucket = fmt.Sprintf("conduit-archv2-gate-early-%s-%s", m.Account, m.RunID)
	m.Prefix = "runs/" + m.RunID
	// Saved first: a failure after CreateBucket must still leave the name
	// in the manifest for cleanup.
	if err := m.save(); err != nil {
		return err
	}
	if err := createBucket(ctx, c.s3, m.Region, m.Bucket, tags); err != nil {
		return err
	}
	logf("bucket %s (private, SSE-S3, TLS-only)", m.Bucket)

	if err := createRole(ctx, c, m, tags); err != nil {
		return err
	}
	if err := createSecurityGroup(ctx, c, m, tags); err != nil {
		return err
	}
	return runInstance(ctx, c, m, tags, rootDevice, instanceOpts{
		GoVersion: *goVersion, MaxMinutes: *maxMinutes, Rounds: *rounds,
		VolumeGB: *volumeGB, VolumeMBps: *volumeMBps, VolumeIOPS: *volumeIOPS,
	})
}

// placeInstance picks the instance type, default VPC, subnet and AMI, and
// returns the AMI's root device name.
func placeInstance(ctx context.Context, c clients, m *manifest, want, fallback string) (string, error) {
	// Instance type, then a subnet in an AZ that offers it.
	it, offeredAZs, err := pickInstanceType(ctx, c.ec2, want, fallback)
	if err != nil {
		return "", err
	}
	m.InstanceType = it
	if it != want {
		m.InstanceTypeNote = fmt.Sprintf("%s is not offered in %s; fell back to %s", want, m.Region, it)
		logf("NOTE: %s", m.InstanceTypeNote)
	}
	vpcs, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("isDefault"), Values: []string{"true"}}},
	})
	if err != nil {
		return "", fmt.Errorf("describe VPCs: %w", err)
	}
	if len(vpcs.Vpcs) == 0 {
		return "", fmt.Errorf("no default VPC in %s; stopping (nothing launched)", m.Region)
	}
	m.VPC = aws.ToString(vpcs.Vpcs[0].VpcId)
	subnets, err := c.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{
		{Name: aws.String("vpc-id"), Values: []string{m.VPC}},
		{Name: aws.String("default-for-az"), Values: []string{"true"}},
	}})
	if err != nil {
		return "", fmt.Errorf("describe subnets: %w", err)
	}
	for _, s := range subnets.Subnets {
		if offeredAZs[aws.ToString(s.AvailabilityZone)] && aws.ToBool(s.MapPublicIpOnLaunch) {
			m.Subnet = aws.ToString(s.SubnetId)
			break
		}
	}
	if m.Subnet == "" {
		return "", fmt.Errorf("no default subnet in %s is in an AZ offering %s", m.VPC, it)
	}

	p, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(amiParameter)})
	if err != nil {
		return "", fmt.Errorf("read %s: %w", amiParameter, err)
	}
	m.AMI = aws.ToString(p.Parameter.Value)
	imgs, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{m.AMI}})
	if err != nil || len(imgs.Images) == 0 {
		return "", fmt.Errorf("describe AMI %s: %w", m.AMI, err)
	}
	rootDevice := aws.ToString(imgs.Images[0].RootDeviceName)
	if err := m.save(); err != nil {
		return "", err
	}
	logf("type %s, AMI %s (%s), VPC %s, subnet %s", it, m.AMI, aws.ToString(imgs.Images[0].Name), m.VPC, m.Subnet)
	return rootDevice, nil
}

func createRole(ctx context.Context, c clients, m *manifest, tags map[string]string) error {
	// IAM role and instance profile.
	m.RoleName = "conduit-archv2-gate-early-" + m.RunID
	m.InlinePolicyName = "put-results-only"
	m.InstanceProfileName = m.RoleName
	if _, err := c.iam.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(m.RoleName),
		AssumeRolePolicyDocument: aws.String(ec2TrustPolicy),
		Description:              aws.String("archv2-gate early benchmark instance: SSM + PutObject to its results prefix"),
		MaxSessionDuration:       aws.Int32(3600),
		Tags:                     iamTags(tags),
	}); err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	if err := m.save(); err != nil {
		return err
	}
	if _, err := c.iam.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName: aws.String(m.RoleName), PolicyArn: aws.String(ssmCorePolicy),
	}); err != nil {
		return fmt.Errorf("attach SSM policy: %w", err)
	}
	putOnly := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::%s/%s/*"}]}`, m.Bucket, m.Prefix)
	if _, err := c.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(m.RoleName), PolicyName: aws.String(m.InlinePolicyName), PolicyDocument: aws.String(putOnly),
	}); err != nil {
		return fmt.Errorf("put inline policy: %w", err)
	}
	if _, err := c.iam.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(m.InstanceProfileName), Tags: iamTags(tags),
	}); err != nil {
		return fmt.Errorf("create instance profile: %w", err)
	}
	if err := m.save(); err != nil {
		return err
	}
	if _, err := c.iam.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(m.InstanceProfileName), RoleName: aws.String(m.RoleName),
	}); err != nil {
		return fmt.Errorf("add role to instance profile: %w", err)
	}
	logf("role and instance profile %s", m.RoleName)
	return nil
}

func createSecurityGroup(ctx context.Context, c clients, m *manifest, tags map[string]string) error {
	// Security group: no ingress rule is ever added; the default egress rule
	// stays so the instance can reach dnf, GitHub, go.dev, S3 and SSM.
	sg, err := c.ec2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:         aws.String("conduit-archv2-gate-early-" + m.RunID),
		Description:       aws.String("archv2-gate early benchmark: no ingress, egress only"),
		VpcId:             aws.String(m.VPC),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: ec2Tags(tags)}},
	})
	if err != nil {
		return fmt.Errorf("create security group: %w", err)
	}
	m.SecurityGroupID = aws.ToString(sg.GroupId)
	if err := m.save(); err != nil {
		return err
	}
	sgs, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{m.SecurityGroupID}})
	if err != nil {
		return fmt.Errorf("describe security group: %w", err)
	}
	if n := len(sgs.SecurityGroups[0].IpPermissions); n != 0 {
		return fmt.Errorf("security group %s has %d ingress rules, want 0", m.SecurityGroupID, n)
	}
	logf("security group %s (0 ingress rules)", m.SecurityGroupID)
	return nil
}

// instanceOpts are the launch flags runInstance needs.
type instanceOpts struct {
	GoVersion                        string
	MaxMinutes, Rounds               int
	VolumeGB, VolumeMBps, VolumeIOPS int
}

func runInstance(ctx context.Context, c clients, m *manifest, tags map[string]string, rootDevice string, o instanceOpts) error {
	goSHA, err := goTarballSHA256(ctx, o.GoVersion)
	if err != nil {
		return err
	}
	var ud strings.Builder
	if err := template.Must(template.New("ud").Parse(userDataTmpl)).Execute(&ud, map[string]any{
		"MaxMinutes": o.MaxMinutes, "Bucket": m.Bucket, "Prefix": m.Prefix, "Region": m.Region,
		"MainSHA": m.MainSHA, "FanoutSHA": m.FanoutSHA, "FanoutPR": m.FanoutPR,
		"HarnessSHA": m.HarnessSHA, "HarnessPR": m.HarnessPR,
		"GoVersion": o.GoVersion, "GoSHA256": goSHA, "Rounds": o.Rounds,
	}); err != nil {
		return fmt.Errorf("render user data: %w", err)
	}

	// IAM is eventually consistent: a fresh instance profile can be rejected
	// by RunInstances for a few seconds after creation.
	input := &ec2.RunInstancesInput{
		ImageId:                           aws.String(m.AMI),
		InstanceType:                      ec2types.InstanceType(m.InstanceType),
		MinCount:                          aws.Int32(1),
		MaxCount:                          aws.Int32(1),
		InstanceInitiatedShutdownBehavior: ec2types.ShutdownBehaviorTerminate,
		IamInstanceProfile:                &ec2types.IamInstanceProfileSpecification{Name: aws.String(m.InstanceProfileName)},
		UserData:                          aws.String(encodeBase64(ud.String())),
		MetadataOptions: &ec2types.InstanceMetadataOptionsRequest{
			HttpTokens:              ec2types.HttpTokensStateRequired,
			HttpPutResponseHopLimit: aws.Int32(1),
		},
		NetworkInterfaces: []ec2types.InstanceNetworkInterfaceSpecification{{
			DeviceIndex:              aws.Int32(0),
			SubnetId:                 aws.String(m.Subnet),
			Groups:                   []string{m.SecurityGroupID},
			AssociatePublicIpAddress: aws.Bool(true),
			DeleteOnTermination:      aws.Bool(true),
		}},
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{{
			DeviceName: aws.String(rootDevice),
			Ebs: &ec2types.EbsBlockDevice{
				VolumeSize:          aws.Int32(int32(o.VolumeGB)), //nolint:gosec // small flag value
				VolumeType:          ec2types.VolumeTypeGp3,
				Throughput:          aws.Int32(int32(o.VolumeMBps)), //nolint:gosec // small flag value
				Iops:                aws.Int32(int32(o.VolumeIOPS)), //nolint:gosec // small flag value
				Encrypted:           aws.Bool(true),
				DeleteOnTermination: aws.Bool(true),
			},
		}},
		TagSpecifications: []ec2types.TagSpecification{
			{ResourceType: ec2types.ResourceTypeInstance, Tags: append(ec2Tags(tags), ec2types.Tag{Key: aws.String("Name"), Value: aws.String("conduit-archv2-gate-early")})},
			{ResourceType: ec2types.ResourceTypeVolume, Tags: ec2Tags(tags)},
			{ResourceType: ec2types.ResourceTypeNetworkInterface, Tags: ec2Tags(tags)},
		},
	}
	var out *ec2.RunInstancesOutput
	for attempt := 1; ; attempt++ {
		out, err = c.ec2.RunInstances(ctx, input)
		if err == nil {
			break
		}
		var ae smithy.APIError
		if attempt < 12 && errors.As(err, &ae) && ae.ErrorCode() == "InvalidParameterValue" &&
			strings.Contains(ae.ErrorMessage(), "iamInstanceProfile") {
			time.Sleep(10 * time.Second)
			continue
		}
		return fmt.Errorf("run instances: %w", err)
	}
	m.InstanceID = aws.ToString(out.Instances[0].InstanceId)
	m.LaunchedAt = time.Now().UTC()
	if err := m.save(); err != nil {
		return err
	}
	logf("instance %s launched; waiting for running", m.InstanceID)

	if err := ec2.NewInstanceRunningWaiter(c.ec2).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{m.InstanceID}}, 5*time.Minute); err != nil {
		return fmt.Errorf("wait for running: %w", err)
	}
	vols, err := c.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: []ec2types.Filter{
		{Name: aws.String("attachment.instance-id"), Values: []string{m.InstanceID}},
	}})
	if err == nil {
		for _, v := range vols.Volumes {
			m.VolumeIDs = append(m.VolumeIDs, aws.ToString(v.VolumeId))
		}
	}
	if err := m.save(); err != nil {
		return err
	}
	logf("running. results: s3://%s/%s/  manifest: %s", m.Bucket, m.Prefix, m.path)
	return nil
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func pickInstanceType(ctx context.Context, c *ec2.Client, want, fallback string) (string, map[string]bool, error) {
	for _, t := range []string{want, fallback} {
		out, err := c.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
			LocationType: ec2types.LocationTypeAvailabilityZone,
			Filters:      []ec2types.Filter{{Name: aws.String("instance-type"), Values: []string{t}}},
		})
		if err != nil {
			return "", nil, fmt.Errorf("describe instance type offerings: %w", err)
		}
		if len(out.InstanceTypeOfferings) > 0 {
			azs := map[string]bool{}
			for _, o := range out.InstanceTypeOfferings {
				azs[aws.ToString(o.Location)] = true
			}
			return t, azs, nil
		}
	}
	return "", nil, fmt.Errorf("neither %s nor %s is offered in this region", want, fallback)
}

func createBucket(ctx context.Context, c *s3.Client, region, bucket string, tags map[string]string) error {
	in := &s3.CreateBucketInput{Bucket: aws.String(bucket), ObjectOwnership: s3types.ObjectOwnershipBucketOwnerEnforced}
	if region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(region)}
	}
	if _, err := c.CreateBucket(ctx, in); err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}
	if _, err := c.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls: aws.Bool(true), IgnorePublicAcls: aws.Bool(true),
			BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("block public access: %w", err)
	}
	if _, err := c.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{Rules: []s3types.ServerSideEncryptionRule{{
			ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{SSEAlgorithm: s3types.ServerSideEncryptionAes256},
		}}},
	}); err != nil {
		return fmt.Errorf("set SSE-S3: %w", err)
	}
	tlsOnly := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"TLSOnly","Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`, bucket)
	if _, err := c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(tlsOnly)}); err != nil {
		return fmt.Errorf("put TLS-only bucket policy: %w", err)
	}
	var ts []s3types.Tag
	for k, v := range tags {
		ts = append(ts, s3types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	if _, err := c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: aws.String(bucket), Tagging: &s3types.Tagging{TagSet: ts}}); err != nil {
		return fmt.Errorf("tag bucket: %w", err)
	}
	return nil
}

// goTarballSHA256 reads the published checksum of the linux/amd64 Go tarball,
// which user data verifies before unpacking.
func goTarballSHA256(ctx context.Context, version string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://go.dev/dl/?mode=json&include=all", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch Go release list: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var releases []struct {
		Files []struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &releases); err != nil {
		return "", fmt.Errorf("parse Go release list: %w", err)
	}
	want := "go" + version + ".linux-amd64.tar.gz"
	for _, r := range releases {
		for _, f := range r.Files {
			if f.Filename == want && len(f.SHA256) == 64 {
				return f.SHA256, nil
			}
		}
	}
	return "", fmt.Errorf("no published checksum for %s", want)
}

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "manifest written by launch (required)")
	_ = fs.Parse(args)
	m, err := loadManifest(*manifestPath)
	if err != nil {
		return err
	}
	c, err := newClients(ctx, m.Region)
	if err != nil {
		return err
	}
	if m.InstanceID != "" {
		out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{m.InstanceID}})
		if err != nil {
			return err
		}
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				fmt.Printf("instance %s: %s (launched %s)\n", m.InstanceID, i.State.Name, aws.ToTime(i.LaunchTime).UTC().Format(time.RFC3339))
			}
		}
	}
	obj, err := c.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(m.Bucket), Key: aws.String(m.Prefix + "/status.txt")})
	if err != nil {
		fmt.Println("status.txt: not uploaded yet")
		return nil
	}
	defer obj.Body.Close()
	raw, _ := io.ReadAll(obj.Body)
	fmt.Print(string(raw))
	return nil
}

func cleanup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "manifest written by launch (required)")
	deleteBucket := fs.Bool("delete-bucket", false, "also empty and delete the results bucket (download results first)")
	_ = fs.Parse(args)
	m, err := loadManifest(*manifestPath)
	if err != nil {
		return err
	}
	c, err := newClients(ctx, m.Region)
	if err != nil {
		return err
	}
	done := func(what string) {
		m.Deleted = append(m.Deleted, what)
		_ = m.save()
		fmt.Fprintln(os.Stderr, "deleted", what)
	}
	var errs []error

	if err := terminateInstance(ctx, c, m); err != nil {
		return err
	}
	if m.SecurityGroupID != "" {
		// The ENI can outlive the instance by a few seconds.
		for attempt := 1; ; attempt++ {
			_, err := c.ec2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: aws.String(m.SecurityGroupID)})
			if err == nil || isCode(err, "InvalidGroup.NotFound") {
				done(m.SecurityGroupID)
				break
			}
			if attempt >= 12 || !isCode(err, "DependencyViolation") {
				errs = append(errs, fmt.Errorf("delete security group: %w", err))
				break
			}
			time.Sleep(10 * time.Second)
		}
	}
	if m.InstanceProfileName != "" {
		_, _ = c.iam.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
			InstanceProfileName: aws.String(m.InstanceProfileName), RoleName: aws.String(m.RoleName),
		})
		if _, err := c.iam.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(m.InstanceProfileName)}); err != nil && !isNoSuchEntity(err) {
			errs = append(errs, fmt.Errorf("delete instance profile: %w", err))
		} else {
			done("instance-profile/" + m.InstanceProfileName)
		}
	}
	if m.RoleName != "" {
		_, _ = c.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(m.RoleName), PolicyArn: aws.String(ssmCorePolicy)})
		_, _ = c.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(m.RoleName), PolicyName: aws.String(m.InlinePolicyName)})
		if _, err := c.iam.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(m.RoleName)}); err != nil && !isNoSuchEntity(err) {
			errs = append(errs, fmt.Errorf("delete role: %w", err))
		} else {
			done("role/" + m.RoleName)
		}
	}
	if *deleteBucket && m.Bucket != "" {
		if err := emptyAndDeleteBucket(ctx, c.s3, m.Bucket); err != nil {
			errs = append(errs, err)
		} else {
			done("bucket/" + m.Bucket)
		}
	}
	return errors.Join(errs...)
}

// terminateInstance terminates the instance unless it already is, and waits
// until DescribeInstances reports it terminated.
func terminateInstance(ctx context.Context, c clients, m *manifest) error {
	if m.InstanceID == "" {
		return nil
	}
	out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{m.InstanceID}})
	if err != nil {
		return fmt.Errorf("describe instance: %w", err)
	}
	state := ec2types.InstanceStateNameTerminated
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			state = i.State.Name
		}
	}
	if state != ec2types.InstanceStateNameTerminated {
		if _, err := c.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{m.InstanceID}}); err != nil {
			return fmt.Errorf("terminate %s: %w", m.InstanceID, err)
		}
	}
	if err := ec2.NewInstanceTerminatedWaiter(c.ec2).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{m.InstanceID}}, 10*time.Minute); err != nil {
		return fmt.Errorf("wait for %s to terminate: %w", m.InstanceID, err)
	}
	fmt.Fprintf(os.Stderr, "instance %s is terminated\n", m.InstanceID)
	return nil
}

func emptyAndDeleteBucket(ctx context.Context, c *s3.Client, bucket string) error {
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list bucket: %w", err)
		}
		for _, o := range page.Contents {
			if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key}); err != nil {
				return fmt.Errorf("delete %s: %w", aws.ToString(o.Key), err)
			}
		}
	}
	if _, err := c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
		return fmt.Errorf("delete bucket: %w", err)
	}
	return nil
}

func isCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

func isNoSuchEntity(err error) bool {
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

func encodeBase64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func ec2Tags(tags map[string]string) []ec2types.Tag {
	var out []ec2types.Tag
	for k, v := range tags {
		out = append(out, ec2types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}

func iamTags(tags map[string]string) []iamtypes.Tag {
	var out []iamtypes.Tag
	for k, v := range tags {
		out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return out
}
