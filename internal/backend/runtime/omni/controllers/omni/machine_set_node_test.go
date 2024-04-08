// Copyright (c) 2024 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

package omni_test

import (
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/kvutils"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	omnictrl "github.com/siderolabs/omni/internal/backend/runtime/omni/controllers/omni"
)

type MachineSetNodeSuite struct {
	OmniSuite
	machinesOffset int
}

func newMachineClass(selectors ...string) *omni.MachineClass {
	id := uuid.New().String()

	cls := omni.NewMachineClass(resources.DefaultNamespace, id)
	cls.TypedSpec().Value.MatchLabels = selectors

	return cls
}

func (suite *MachineSetNodeSuite) createMachines(labels ...map[string]string) []*omni.MachineStatus {
	res := make([]*omni.MachineStatus, 0, len(labels))

	for i, l := range labels {
		id := fmt.Sprintf("machine%d", suite.machinesOffset+i)

		machine := omni.NewMachine(resources.DefaultNamespace, id)

		machineStatus := omni.NewMachineStatus(resources.DefaultNamespace, id)

		machineStatus.Metadata().Labels().Do(func(temp kvutils.TempKV) {
			for k, v := range l {
				temp.Set(k, v)
			}
		})

		machineStatus.TypedSpec().Value.TalosVersion = "v1.6.0"

		res = append(res, machineStatus)

		suite.Require().NoError(suite.state.Create(suite.ctx, machineStatus))
		suite.Require().NoError(suite.state.Create(suite.ctx, machine))
	}

	suite.machinesOffset += len(res)

	return res
}

func (suite *MachineSetNodeSuite) TestReconcile() {
	suite.startRuntime()

	suite.Require().NoError(suite.runtime.RegisterController(&omnictrl.MachineSetNodeController{}))

	machines := suite.createMachines(
		map[string]string{
			omni.MachineStatusLabelArch:      "amd64",
			omni.MachineStatusLabelAvailable: "",
		},
		map[string]string{
			omni.MachineStatusLabelAvailable: "",
		},
		map[string]string{},
		map[string]string{
			omni.MachineStatusLabelCPU:       "AMD",
			omni.MachineStatusLabelAvailable: "",
		},
	)

	machineSet := omni.NewMachineSet(resources.DefaultNamespace, "auto")

	assertMachineSetNode := func(machine *omni.MachineStatus) {
		assertResource[*omni.MachineSetNode](
			&suite.OmniSuite,
			omni.NewMachineSetNode(
				resources.DefaultNamespace,
				machine.Metadata().ID(),
				machineSet).Metadata(),
			func(*omni.MachineSetNode, *assert.Assertions) {},
		)
	}

	assertNoMachineSetNode := func(machine *omni.MachineStatus) {
		assertNoResource[*omni.MachineSetNode](
			&suite.OmniSuite,
			omni.NewMachineSetNode(
				resources.DefaultNamespace,
				machine.Metadata().ID(),
				machineSet),
		)
	}

	cluster := omni.NewCluster(resources.DefaultNamespace, "cluster1")
	cluster.TypedSpec().Value.TalosVersion = "1.6.0"

	suite.Require().NoError(suite.state.Create(suite.ctx, cluster))

	machineSet.Metadata().Labels().Set(omni.LabelCluster, cluster.Metadata().ID())
	machineSet.Metadata().Labels().Set(omni.LabelWorkerRole, "")

	machineClass := newMachineClass(fmt.Sprintf("%s==amd64", omni.MachineStatusLabelArch))

	machineSet.TypedSpec().Value.MachineClass = &specs.MachineSetSpec_MachineClass{
		Name:         machineClass.Metadata().ID(),
		MachineCount: 5,
	}

	suite.Require().NoError(suite.state.Create(suite.ctx, machineClass))
	suite.Require().NoError(suite.state.Create(suite.ctx, machineSet))

	assertMachineSetNode(machines[0])

	machineClass = newMachineClass(fmt.Sprintf("%s==AMD", omni.MachineStatusLabelCPU))

	suite.Require().NoError(suite.state.Create(suite.ctx, machineClass))

	machineSet.TypedSpec().Value.MachineClass.Name = machineClass.Metadata().ID()

	suite.Require().NoError(suite.state.Update(suite.ctx, machineSet))

	// no changes after updating machine set machine class
	assertNoMachineSetNode(machines[3])
	assertMachineSetNode(machines[0])

	machineSet.TypedSpec().Value.MachineClass.MachineCount = 0

	suite.Require().NoError(suite.state.Update(suite.ctx, machineSet))

	// scale down to 0 should remove machine set node
	assertNoMachineSetNode(machines[0])

	machineSet.TypedSpec().Value.MachineClass.MachineCount = 3

	suite.Require().NoError(suite.state.Update(suite.ctx, machineSet))

	// scale back up to 3 after changing the machine class
	// should create a machine set node for the 3rd machine
	assertMachineSetNode(machines[3])
	assertNoMachineSetNode(machines[0])

	// add more machines and wait for the controller to scale up
	machines = append(machines, suite.createMachines(
		map[string]string{
			omni.MachineStatusLabelCPU:       "AMD",
			omni.MachineStatusLabelAvailable: "",
		},
		map[string]string{
			omni.MachineStatusLabelCPU: "AMD",
		},
		map[string]string{
			omni.MachineStatusLabelCPU:       "AMD",
			omni.MachineStatusLabelAvailable: "",
		},
	)...)

	assertMachineSetNode(machines[4])
	assertMachineSetNode(machines[6])
	assertNoMachineSetNode(machines[5])

	suite.Require().NoError(suite.state.Destroy(suite.ctx, omni.NewMachine(resources.DefaultNamespace, machines[4].Metadata().ID()).Metadata()))

	assertNoMachineSetNode(machines[4])
}

func TestSortFunction(t *testing.T) {
	machineStatuses := map[resource.ID]*omni.MachineStatus{}
	machineSetNodes := make([]*omni.MachineSetNode, 0, 10)

	for i := 0; i < 10; i++ {
		id := strconv.Itoa(i)

		machineStatuses[id] = omni.NewMachineStatus(resources.DefaultNamespace, id)

		machineSetNode := omni.NewMachineSetNode(resources.DefaultNamespace, id, omni.NewMachineSet(resources.DefaultNamespace, "ms"))
		machineSetNode.Metadata().SetCreated(time.Now())

		machineSetNodes = append(machineSetNodes, machineSetNode)
	}

	slices.SortStableFunc(machineSetNodes, omnictrl.GetMachineSetNodeSortFunction(machineStatuses))

	require := require.New(t)

	for i := 0; i < len(machineSetNodes)-1; i++ {
		require.Equal(-1, machineSetNodes[i].Metadata().Created().Compare(machineSetNodes[i+1].Metadata().Created()))
	}

	machineStatuses["9"].Metadata().Labels().Set(omni.MachineStatusLabelDisconnected, "")

	slices.SortStableFunc(machineSetNodes, omnictrl.GetMachineSetNodeSortFunction(machineStatuses))

	machineStatuses["2"].Metadata().Labels().Set(omni.MachineStatusLabelDisconnected, "")

	slices.SortStableFunc(machineSetNodes, omnictrl.GetMachineSetNodeSortFunction(machineStatuses))

	require.Equal("2", machineSetNodes[0].Metadata().ID())
	require.Equal("9", machineSetNodes[1].Metadata().ID())
}

func TestMachineSetNodeSuite(t *testing.T) {
	suite.Run(t, new(MachineSetNodeSuite))
}
