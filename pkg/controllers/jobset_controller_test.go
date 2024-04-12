/*
Copyright 2023 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    htcp://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2/ktesting"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	"sigs.k8s.io/jobset/pkg/constants"
	testutils "sigs.k8s.io/jobset/pkg/util/testing"
)

func TestIsJobFinished(t *testing.T) {
	tests := []struct {
		name              string
		conditions        []batchv1.JobCondition
		finished          bool
		wantConditionType batchv1.JobConditionType
	}{
		{
			name: "succeeded",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionTrue,
				},
			},
			finished:          true,
			wantConditionType: batchv1.JobComplete,
		},
		{
			name: "failed",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailed,
					Status: corev1.ConditionTrue,
				},
			},
			finished:          true,
			wantConditionType: batchv1.JobFailed,
		},
		{
			name: "active",
			conditions: []batchv1.JobCondition{
				{
					Type:   "",
					Status: corev1.ConditionTrue,
				},
			},
			finished:          false,
			wantConditionType: "",
		},
		{
			name: "suspended",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobSuspended,
					Status: corev1.ConditionTrue,
				},
			},
			finished:          false,
			wantConditionType: "",
		},
		{
			name: "failure target",
			conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobFailureTarget,
					Status: corev1.ConditionTrue,
				},
			},
			finished:          false,
			wantConditionType: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			finished, conditionType := JobFinished(&batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: tc.conditions,
				},
			})
			if diff := cmp.Diff(tc.finished, finished); diff != "" {
				t.Errorf("unexpected finished value (+got/-want): %s", diff)
			}
			if diff := cmp.Diff(tc.wantConditionType, conditionType); diff != "" {
				t.Errorf("unexpected condition type (+got/-want): %s", diff)
			}
		})
	}
}

func TestConstructJobsFromTemplate(t *testing.T) {
	var (
		jobSetName        = "test-jobset"
		replicatedJobName = "replicated-job"
		jobName           = "test-job"
		ns                = "default"
		topologyDomain    = "test-topology-domain"
	)

	tests := []struct {
		name      string
		js        *jobset.JobSet
		ownedJobs *childJobs
		want      []*batchv1.Job
	}{
		{
			name: "no jobs created",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			ownedJobs: &childJobs{
				active: []*batchv1.Job{
					testutils.MakeJob("test-jobset-replicated-job-0", ns).Obj(),
				},
			},
		},
		{
			name: "all jobs created",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-0",
					ns:                ns,
					replicas:          2,
					jobIdx:            0}).
					Suspend(false).Obj(),
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "one job created, one job not created (already active)",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).Obj(),
			ownedJobs: &childJobs{
				active: []*batchv1.Job{
					testutils.MakeJob("test-jobset-replicated-job-0", ns).Obj(),
				},
			},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "one job created, one job not created (already succeeded)",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).Obj(),
			ownedJobs: &childJobs{
				successful: []*batchv1.Job{
					testutils.MakeJob("test-jobset-replicated-job-0", ns).Obj(),
				},
			},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "one job created, one job not created (already failed)",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).Obj(),
			ownedJobs: &childJobs{
				failed: []*batchv1.Job{
					testutils.MakeJob("test-jobset-replicated-job-0", ns).Obj(),
				},
			},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "one job created, one job not created (marked for deletion)",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).Obj(),
			ownedJobs: &childJobs{
				delete: []*batchv1.Job{
					testutils.MakeJob("test-jobset-replicated-job-0", ns).Obj(),
				},
			},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "multiple replicated jobs",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-A").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-B").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(2).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{
				active: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-B",
						jobName:           "test-jobset-replicated-job-B-0",
						ns:                ns,
						replicas:          2,
						jobIdx:            0}).
						Suspend(false).Obj(),
				},
			},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: "replicated-job-A",
					jobName:           "test-jobset-replicated-job-A-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).Obj(),
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: "replicated-job-B",
					jobName:           "test-jobset-replicated-job-B-1",
					ns:                ns,
					replicas:          2,
					jobIdx:            1}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "exclusive placement for a ReplicatedJob",
			js: testutils.MakeJobSet(jobSetName, ns).
				// Replicated Job A has exclusive placement annotation.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-A").
					Job(testutils.MakeJobTemplate(jobName, ns).
						SetAnnotations(map[string]string{jobset.ExclusiveKey: topologyDomain}).
						Obj()).
					Replicas(1).
					Obj()).
				// Replicated Job B has no exclusive placement annotation.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-B").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName + "-A",
					jobName:           "test-jobset-replicated-job-A-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0,
					topology:          topologyDomain}).
					Suspend(false).Obj(),
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName + "-B",
					jobName:           "test-jobset-replicated-job-B-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "exclusive placement using nodeSelectorStrategy for a ReplicatedJob",
			js: testutils.MakeJobSet(jobSetName, ns).
				// Replicated Job A has exclusive placement annotation.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-A").
					Job(testutils.MakeJobTemplate(jobName, ns).
						SetAnnotations(map[string]string{
							jobset.ExclusiveKey:            topologyDomain,
							jobset.NodeSelectorStrategyKey: "true"}).
						Obj()).
					Replicas(1).
					Obj()).
				// Replicated Job B has no exclusive placement annotation.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-B").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:           jobSetName,
					replicatedJobName:    replicatedJobName + "-A",
					jobName:              "test-jobset-replicated-job-A-0",
					ns:                   ns,
					replicas:             1,
					jobIdx:               0,
					topology:             topologyDomain,
					nodeSelectorStrategy: true}).
					Suspend(false).
					NodeSelector(map[string]string{
						jobset.NamespacedJobKey: namespacedJobName(ns, "test-jobset-replicated-job-A-0"),
					}).
					Tolerations([]corev1.Toleration{
						{
							Key:      jobset.NoScheduleTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					}).
					Obj(),
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName + "-B",
					jobName:           "test-jobset-replicated-job-B-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "exclusive placement for entire JobSet",
			js: testutils.MakeJobSet(jobSetName, ns).
				SetAnnotations(map[string]string{jobset.ExclusiveKey: topologyDomain}).
				// Replicated Job A has.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-A").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				// Replicated Job B.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-B").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName + "-A",
					jobName:           "test-jobset-replicated-job-A-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0,
					topology:          topologyDomain}).
					Suspend(false).Obj(),
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName + "-B",
					jobName:           "test-jobset-replicated-job-B-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0,
					topology:          topologyDomain}).
					Suspend(false).Obj(),
			},
		},
		{
			name: "exclusive placement using nodeSelectorStrategy for entire JobSet",
			js: testutils.MakeJobSet(jobSetName, ns).
				SetAnnotations(map[string]string{
					jobset.ExclusiveKey:            topologyDomain,
					jobset.NodeSelectorStrategyKey: "true",
				}).
				// Replicated Job A has.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-A").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				// Replicated Job B.
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName + "-B").
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:           jobSetName,
					replicatedJobName:    replicatedJobName + "-A",
					jobName:              "test-jobset-replicated-job-A-0",
					ns:                   ns,
					replicas:             1,
					jobIdx:               0,
					topology:             topologyDomain,
					nodeSelectorStrategy: true}).
					Suspend(false).
					NodeSelector(map[string]string{
						jobset.NamespacedJobKey: namespacedJobName(ns, "test-jobset-replicated-job-A-0"),
					}).
					Tolerations([]corev1.Toleration{
						{
							Key:      jobset.NoScheduleTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					}).
					Obj(),
				makeJob(&makeJobArgs{
					jobSetName:           jobSetName,
					replicatedJobName:    replicatedJobName + "-B",
					jobName:              "test-jobset-replicated-job-B-0",
					ns:                   ns,
					replicas:             1,
					jobIdx:               0,
					topology:             topologyDomain,
					nodeSelectorStrategy: true}).
					Suspend(false).
					NodeSelector(map[string]string{
						jobset.NamespacedJobKey: namespacedJobName(ns, "test-jobset-replicated-job-B-0"),
					}).
					Tolerations([]corev1.Toleration{
						{
							Key:      jobset.NoScheduleTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					}).
					Obj(),
			},
		},
		{
			name: "pod dns hostnames enabled",
			js: testutils.MakeJobSet(jobSetName, ns).
				EnableDNSHostnames(true).
				NetworkSubdomain(jobSetName).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Subdomain(jobSetName).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).
					Subdomain(jobSetName).Obj(),
			},
		},
		{
			name: "suspend job set",
			js: testutils.MakeJobSet(jobSetName, ns).
				Suspend(true).
				EnableDNSHostnames(true).
				NetworkSubdomain(jobSetName).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Subdomain(jobSetName).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(true).
					Subdomain(jobSetName).Obj(),
			},
		},
		{
			name: "resume job set",
			js: testutils.MakeJobSet(jobSetName, ns).
				Suspend(false).
				EnableDNSHostnames(true).
				NetworkSubdomain(jobSetName).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Subdomain(jobSetName).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).
					Subdomain(jobSetName).Obj(),
			},
		},
		{
			name: "node selector exclusive placement strategy enabled",
			js: testutils.MakeJobSet(jobSetName, ns).
				EnableDNSHostnames(true).
				NetworkSubdomain(jobSetName).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).
						SetAnnotations(map[string]string{
							jobset.ExclusiveKey:            topologyDomain,
							jobset.NodeSelectorStrategyKey: "true",
						}).
						Obj()).
					Subdomain(jobSetName).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:           jobSetName,
					replicatedJobName:    replicatedJobName,
					jobName:              "test-jobset-replicated-job-0",
					ns:                   ns,
					replicas:             1,
					jobIdx:               0,
					topology:             topologyDomain,
					nodeSelectorStrategy: true}).
					Suspend(false).
					Subdomain(jobSetName).
					NodeSelector(map[string]string{
						jobset.NamespacedJobKey: namespacedJobName(ns, "test-jobset-replicated-job-0"),
					}).
					Tolerations([]corev1.Toleration{
						{
							Key:      jobset.NoScheduleTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					}).Obj(),
			},
		},
		{
			name: "startup-policy",
			js: testutils.MakeJobSet(jobSetName, ns).
				StartupPolicy(&jobset.StartupPolicy{
					StartupPolicyOrder: jobset.InOrder,
				}).
				EnableDNSHostnames(true).
				NetworkSubdomain(jobSetName).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Subdomain(jobSetName).
					Replicas(1).
					Obj()).
				Obj(),
			ownedJobs: &childJobs{},
			want: []*batchv1.Job{
				makeJob(&makeJobArgs{
					jobSetName:        jobSetName,
					replicatedJobName: replicatedJobName,
					jobName:           "test-jobset-replicated-job-0",
					ns:                ns,
					replicas:          1,
					jobIdx:            0}).
					Suspend(false).
					Subdomain(jobSetName).Obj(),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []*batchv1.Job
			for _, rjob := range tc.js.Spec.ReplicatedJobs {
				jobs, err := constructJobsFromTemplate(tc.js, &rjob, tc.ownedJobs)
				if err != nil {
					t.Errorf("constructJobsFromTemplate() error = %v", err)
					return
				}
				got = append(got, jobs...)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("constructJobsFromTemplate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateConditions(t *testing.T) {
	var (
		jobSetName        = "test-jobset"
		replicatedJobName = "replicated-job"
		jobName           = "test-job"
		ns                = "default"
	)

	tests := []struct {
		name           string
		js             *jobset.JobSet
		conditions     []metav1.Condition
		newCondition   metav1.Condition
		forceUpdate    bool
		expectedUpdate bool
	}{
		{
			name: "no condition",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{},
			conditions:     []metav1.Condition{},
			expectedUpdate: false,
		},
		{
			name: "do not update if false",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Status: metav1.ConditionFalse, Type: string(jobset.JobSetSuspended), Reason: "JobsResumed"},
			conditions:     []metav1.Condition{},
			expectedUpdate: false,
		},
		{
			name: "force update if false",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Status: metav1.ConditionFalse, Type: string(jobset.JobSetStartupPolicyCompleted), Reason: "StartupPolicy"},
			conditions:     []metav1.Condition{},
			expectedUpdate: true,
			forceUpdate:    true,
		},
		{
			name: "update if condition is true",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Status: metav1.ConditionTrue, Type: string(jobset.JobSetSuspended), Reason: "JobsResumed"},
			conditions:     []metav1.Condition{},
			expectedUpdate: true,
		},

		{
			name: "suspended",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Status: metav1.ConditionTrue, Type: string(jobset.JobSetSuspended), Reason: "JobsSuspended"},
			conditions:     []metav1.Condition{},
			expectedUpdate: true,
		},
		{
			name: "resumed",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Type: string(jobset.JobSetSuspended), Reason: "JobsResumed", Status: metav1.ConditionStatus(corev1.ConditionFalse)},
			conditions:     []metav1.Condition{{Type: string(jobset.JobSetSuspended), Reason: "JobsSuspended", Status: metav1.ConditionStatus(corev1.ConditionTrue)}},
			expectedUpdate: true,
		},
		{
			name: "duplicateComplete",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob(replicatedJobName).
					Job(testutils.MakeJobTemplate(jobName, ns).Obj()).
					Replicas(1).
					Obj()).Obj(),
			newCondition:   metav1.Condition{Type: string(jobset.JobSetCompleted), Message: "Jobs completed", Reason: "JobsCompleted", Status: metav1.ConditionTrue},
			conditions:     []metav1.Condition{{Type: string(jobset.JobSetCompleted), Message: "Jobs completed", Reason: "JobsCompleted", Status: metav1.ConditionTrue}},
			expectedUpdate: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jsWithConditions := tc.js
			jsWithConditions.Status.Conditions = tc.conditions
			gotUpdate := updateCondition(jsWithConditions, tc.newCondition, tc.forceUpdate)
			if gotUpdate != tc.expectedUpdate {
				t.Errorf("updateCondition return mismatch")
			}
		})
	}
}

func TestCalculateReplicatedJobStatuses(t *testing.T) {
	var (
		jobSetName = "test-jobset"
		ns         = "default"
	)
	tests := []struct {
		name     string
		js       *jobset.JobSet
		jobs     childJobs
		expected []jobset.ReplicatedJobStatus
	}{
		{
			name: "partial jobs are ready, no succeeded jobs",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				active: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-1-test-job-0",
						ns:                ns,
						replicas:          1,
						jobIdx:            0}).
						Parallelism(1).
						Completions(2).
						Ready(1).
						Succeeded(1).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0",
						ns:                ns,
						replicas:          3,
						jobIdx:            0}).
						Parallelism(5).
						Ready(2).
						Succeeded(3).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-1",
						ns:                ns,
						replicas:          3,
						jobIdx:            0}).
						Parallelism(3).
						Completions(2).
						Ready(1).
						Succeeded(1).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-2",
						ns:                ns,
						replicas:          3,
						jobIdx:            0}).
						Parallelism(2).
						Completions(3).
						Ready(2).
						Succeeded(1).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-3",
						ns:                ns,
						replicas:          3,
						jobIdx:            0}).
						Parallelism(4).
						Completions(5).
						Ready(2).
						Succeeded(1).Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:      "replicated-job-1",
					Ready:     1,
					Succeeded: 0,
				},
				{
					Name:      "replicated-job-2",
					Ready:     3,
					Succeeded: 0,
				},
			},
		},
		{
			name: "no jobs created",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:      "replicated-job-1",
					Ready:     0,
					Succeeded: 0,
				},
				{
					Name:      "replicated-job-2",
					Ready:     0,
					Succeeded: 0,
				},
			},
		},
		{
			name: "partial jobs created",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				active: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0",
						ns:                ns,
						replicas:          3,
						jobIdx:            0}).
						Parallelism(5).
						Ready(2).
						Succeeded(3).Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:      "replicated-job-1",
					Ready:     0,
					Succeeded: 0,
				},
				{
					Name:      "replicated-job-2",
					Ready:     1,
					Succeeded: 0,
				},
			},
		},
		{
			name: "no ready jobs, only succeeded jobs",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				successful: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-1-test-job-0"}).Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:      "replicated-job-1",
					Ready:     0,
					Succeeded: 1,
				},
				{
					Name:      "replicated-job-2",
					Ready:     0,
					Succeeded: 1,
				},
			},
		},
		{
			name: "no ready jobs, only failed jobs",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				failed: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-1-test-job-0"}).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-1-test-job-1"}).Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-1-test-job-2"}).Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:   "replicated-job-1",
					Ready:  0,
					Failed: 3,
				},
				{
					Name:   "replicated-job-2",
					Ready:  0,
					Failed: 1,
				},
			},
		},
		{
			name: "active jobs",
			js: testutils.MakeJobSet(jobSetName, ns).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				active: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).
						Parallelism(5).
						Active(1).
						Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).
						Parallelism(5).
						Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-1"}).
						Parallelism(1).
						Active(1).
						Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:   "replicated-job-1",
					Ready:  0,
					Active: 1,
				},
				{
					Name:   "replicated-job-2",
					Ready:  0,
					Active: 1,
				},
			},
		},
		{
			name: "suspended jobs",
			js: testutils.MakeJobSet(jobSetName, ns).Suspend(true).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-1").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(1).
					Obj()).
				ReplicatedJob(testutils.MakeReplicatedJob("replicated-job-2").
					Job(testutils.MakeJobTemplate("test-job", ns).Obj()).
					Replicas(3).
					Obj()).Obj(),
			jobs: childJobs{
				active: []*batchv1.Job{
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-1",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).
						Parallelism(5).
						Suspend(true).
						Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-0"}).
						Parallelism(5).
						Obj(),
					makeJob(&makeJobArgs{
						jobSetName:        jobSetName,
						replicatedJobName: "replicated-job-2",
						jobName:           "test-jobset-replicated-job-2-test-job-1"}).
						Parallelism(1).
						Suspend(true).
						Obj(),
				},
			},
			expected: []jobset.ReplicatedJobStatus{
				{
					Name:      "replicated-job-1",
					Ready:     0,
					Suspended: 1,
				},
				{
					Name:      "replicated-job-2",
					Ready:     0,
					Suspended: 1,
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := JobSetReconciler{Client: (fake.NewClientBuilder()).Build()}
			statuses := r.calculateReplicatedJobStatuses(context.TODO(), tc.js, &tc.jobs)
			var less interface{} = func(a, b jobset.ReplicatedJobStatus) bool {
				return a.Name < b.Name
			}
			if diff := cmp.Diff(tc.expected, statuses, cmpopts.SortSlices(less)); diff != "" {
				t.Errorf("calculateReplicatedJobStatuses() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFindFirstFailedJob(t *testing.T) {
	testCases := []struct {
		name       string
		failedJobs []*batchv1.Job
		expected   *batchv1.Job
	}{
		{
			name:       "No failed jobs",
			failedJobs: []*batchv1.Job{},
			expected:   nil,
		},
		{
			name: "Single failed job",
			failedJobs: []*batchv1.Job{
				jobWithFailedCondition("job1", time.Now().Add(-1*time.Hour)),
			},
			expected: jobWithFailedCondition("job1", time.Now().Add(-1*time.Hour)),
		},
		{
			name: "Multiple failed jobs, earliest first",
			failedJobs: []*batchv1.Job{
				jobWithFailedCondition("job1", time.Now().Add(-3*time.Hour)),
				jobWithFailedCondition("job2", time.Now().Add(-5*time.Hour)),
			},
			expected: jobWithFailedCondition("job2", time.Now().Add(-5*time.Hour)),
		},
		{
			name: "Jobs without failed condition",
			failedJobs: []*batchv1.Job{
				{ObjectMeta: metav1.ObjectMeta{Name: "job1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "job2"}},
			},
			expected: nil,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := findFirstFailedJob(tc.failedJobs)
			if result != nil && tc.expected != nil {
				assert.Equal(t, result.Name, tc.expected.Name)
			} else if result != nil && tc.expected == nil || result == nil && tc.expected != nil {
				t.Errorf("Expected: %v, got: %v)", result, tc.expected)
			}
		})
	}
}

// Helper function to create a job object with a failed condition
func jobWithFailedCondition(name string, failureTime time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(failureTime),
				},
			},
		},
	}
}

type makeJobArgs struct {
	jobSetName           string
	replicatedJobName    string
	jobName              string
	ns                   string
	replicas             int
	jobIdx               int
	restarts             int
	topology             string
	nodeSelectorStrategy bool
}

// Helper function to create a Job for unit testing.
func makeJob(args *makeJobArgs) *testutils.JobWrapper {
	labels := map[string]string{
		jobset.JobSetNameKey:         args.jobSetName,
		jobset.ReplicatedJobNameKey:  args.replicatedJobName,
		jobset.ReplicatedJobReplicas: strconv.Itoa(args.replicas),
		jobset.JobIndexKey:           strconv.Itoa(args.jobIdx),
		constants.RestartsKey:        strconv.Itoa(args.restarts),
		jobset.JobKey:                jobHashKey(args.ns, args.jobName),
	}
	annotations := map[string]string{
		jobset.JobSetNameKey:         args.jobSetName,
		jobset.ReplicatedJobNameKey:  args.replicatedJobName,
		jobset.ReplicatedJobReplicas: strconv.Itoa(args.replicas),
		jobset.JobIndexKey:           strconv.Itoa(args.jobIdx),
		constants.RestartsKey:        strconv.Itoa(args.restarts),
		jobset.JobKey:                jobHashKey(args.ns, args.jobName),
	}
	// Only set exclusive key if we are using exclusive placement per topology.
	if args.topology != "" {
		annotations[jobset.ExclusiveKey] = args.topology
		// Exclusive placement topology domain must be set in order to use the node selector strategy.
		if args.nodeSelectorStrategy {
			annotations[jobset.NodeSelectorStrategyKey] = "true"
		}
	}
	jobWrapper := testutils.MakeJob(args.jobName, args.ns).
		JobLabels(labels).
		JobAnnotations(annotations).
		PodLabels(labels).
		PodAnnotations(annotations)
	return jobWrapper
}

func TestCreateHeadlessSvcIfNecessary(t *testing.T) {
	var (
		jobSetName = "test-jobset"
		ns         = "default"
	)

	tests := []struct {
		name                           string
		jobSet                         *jobset.JobSet
		existingService                *corev1.Service
		expectServiceCreate            bool
		expectServiceName              string
		expectPublishNotReadyAddresses bool
	}{
		{
			name:   "headless service should not be created",
			jobSet: testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(false).Obj(),
		},
		{
			name:   "headless service exists and should not be created",
			jobSet: testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(true).Obj(),
			existingService: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-jobset",
					Namespace: ns,
				},
			},
		},
		{
			name:                "service does not exist and should be created, subdomain not set",
			jobSet:              testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(true).Obj(),
			expectServiceCreate: true,
			expectServiceName:   "test-jobset",
		},
		{
			name:                "service does not exist and should be created, subdomain set",
			jobSet:              testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(true).NetworkSubdomain("test-subdomain").Obj(),
			expectServiceCreate: true,
			expectServiceName:   "test-subdomain",
		},
		{
			name:                           "service does exist and should be created, publishNotReadyAddresses is false",
			jobSet:                         testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(true).PublishNotReadyAddresses(false).Obj(),
			expectServiceCreate:            true,
			expectServiceName:              "test-jobset",
			expectPublishNotReadyAddresses: false,
		},
		{
			name:                           "service does not exist and should be created, publishNotReadyAddresses is true",
			jobSet:                         testutils.MakeJobSet(jobSetName, ns).EnableDNSHostnames(true).PublishNotReadyAddresses(true).Obj(),
			expectServiceCreate:            true,
			expectServiceName:              "test-jobset",
			expectPublishNotReadyAddresses: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ctx := ktesting.NewTestContext(t)
			scheme := runtime.NewScheme()
			utilruntime.Must(jobset.AddToScheme(scheme))
			utilruntime.Must(corev1.AddToScheme(scheme))
			utilruntime.Must(batchv1.AddToScheme(scheme))
			fakeClientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.existingService != nil {
				fakeClientBuilder.WithObjects(tc.existingService)
			}
			fakeClient := fakeClientBuilder.Build()

			// Create a JobSetReconciler instance with the fake client
			reconciler := &JobSetReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Execute the function under test
			gotErr := reconciler.createHeadlessSvcIfNecessary(ctx, tc.jobSet)
			if gotErr != nil {
				t.Errorf("createHeadlessSvcIfNecessary() error = %v", gotErr)
			}
			if tc.expectServiceCreate {
				svc := &corev1.Service{}
				gotErr = fakeClient.Get(ctx, types.NamespacedName{Name: tc.expectServiceName, Namespace: ns}, svc)
				if gotErr != nil {
					t.Errorf("expected service to be created but got an error: %v", gotErr)
				}
				if svc.OwnerReferences == nil || len(svc.OwnerReferences) == 0 {
					t.Error("expected service to have owner reference set")
				}
				if svc.OwnerReferences[0].Name != tc.jobSet.Name {
					t.Errorf("expected owner reference to be %q, got %q", tc.jobSet.Name, svc.OwnerReferences[0].Name)
				}
				if svc.OwnerReferences[0].Kind != "JobSet" {
					t.Errorf("expected owner reference kind to be JobSet, got %q", svc.OwnerReferences[0].Kind)
				}
				if svc.Spec.ClusterIP != corev1.ClusterIPNone {
					t.Errorf("expected service to have ClusterIPNone, got %s", svc.Spec.ClusterIP)
				}
				selector, ok := svc.Spec.Selector[jobset.JobSetNameKey]
				if !ok {
					t.Errorf("expected service selector to contain %q key", jobset.JobSetNameKey)
				}
				if selector != tc.jobSet.Name {
					t.Errorf("expected service selector to be %q, got %q", tc.jobSet.Name, selector)
				}
				if svc.Spec.PublishNotReadyAddresses != tc.expectPublishNotReadyAddresses {
					t.Errorf("expected PublishNotReadyAddresses to be %t, got %t", tc.expectPublishNotReadyAddresses, svc.Spec.PublishNotReadyAddresses)
				}
			}
		})
	}
}
