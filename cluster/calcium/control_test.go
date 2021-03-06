package calcium

import (
	"bytes"
	"context"
	"io/ioutil"
	"testing"

	"github.com/projecteru2/core/cluster"
	enginemocks "github.com/projecteru2/core/engine/mocks"
	lockmocks "github.com/projecteru2/core/lock/mocks"
	storemocks "github.com/projecteru2/core/store/mocks"
	"github.com/projecteru2/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestControlStart(t *testing.T) {
	c := NewTestCluster()
	ctx := context.Background()
	store := &storemocks.Store{}
	lock := &lockmocks.DistributedLock{}
	lock.On("Lock", mock.Anything).Return(nil)
	lock.On("Unlock", mock.Anything).Return(nil)
	c.store = store
	store.On("CreateLock", mock.Anything, mock.Anything).Return(lock, nil)
	// failed by GetContainers
	store.On("GetContainers", mock.Anything, mock.Anything).Return(nil, types.ErrNoETCD).Once()
	ch, err := c.ControlContainer(ctx, []string{"id1"}, "", true)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	container := &types.Container{
		ID:         "id1",
		Privileged: true,
	}
	engine := &enginemocks.API{}
	container.Engine = engine
	store.On("GetContainers", mock.Anything, mock.Anything).Return([]*types.Container{container}, nil)
	// failed by type
	ch, err = c.ControlContainer(ctx, []string{"id1"}, "", true)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	// failed by start
	engine.On("VirtualizationStart", mock.Anything, mock.Anything).Return(types.ErrNilEngine).Once()
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	engine.On("VirtualizationStart", mock.Anything, mock.Anything).Return(nil)
	// failed by ExecCreate
	hook := &types.Hook{
		AfterStart: []string{"cmd1", "cmd2"},
	}
	container.Hook = hook
	container.Hook.Force = false
	engine.On("ExecCreate", mock.Anything, mock.Anything, mock.Anything).Return("", types.ErrNilEngine).Times(3)
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.NoError(t, r.Error)
	}
	// force false, get no error
	container.Hook.Force = true
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
		assert.Equal(t, r.ContainerID, "id1")
	}
	engine.On("ExecCreate", mock.Anything, mock.Anything, mock.Anything).Return("eid", nil)
	// failed by ExecAttach
	engine.On("ExecAttach", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, nil, types.ErrNilEngine).Once()
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	data := ioutil.NopCloser(bytes.NewBufferString("output"))
	engine.On("ExecAttach", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(data, nil, nil).Twice()
	// failed by ExecExitCode
	engine.On("ExecExitCode", mock.Anything, mock.Anything).Return(-1, types.ErrNilEngine).Once()
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	// exitCode is not 0
	engine.On("ExecExitCode", mock.Anything, mock.Anything).Return(-1, nil).Once()
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	// exitCode is 0
	engine.On("ExecExitCode", mock.Anything, mock.Anything).Return(0, nil)
	engine.On("ExecAttach", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(ioutil.NopCloser(bytes.NewBufferString("succ")), nil, nil)
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.NoError(t, r.Error)
	}
}

func TestControlStop(t *testing.T) {
	c := NewTestCluster()
	ctx := context.Background()
	store := &storemocks.Store{}
	lock := &lockmocks.DistributedLock{}
	lock.On("Lock", mock.Anything).Return(nil)
	lock.On("Unlock", mock.Anything).Return(nil)
	c.store = store
	store.On("CreateLock", mock.Anything, mock.Anything).Return(lock, nil)
	container := &types.Container{
		ID:         "id1",
		Privileged: true,
	}
	engine := &enginemocks.API{}
	container.Engine = engine
	store.On("GetContainers", mock.Anything, mock.Anything).Return([]*types.Container{container}, nil)
	// failed, hook true, remove always false
	hook := &types.Hook{
		BeforeStop: []string{"cmd1"},
	}
	container.Hook = hook
	container.Hook.Force = true
	engine.On("ExecCreate", mock.Anything, mock.Anything, mock.Anything).Return("", types.ErrNilEngine)
	ch, err := c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStop, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	// stop failed
	container.Hook.Force = false
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStop, false)
	engine.On("VirtualizationStop", mock.Anything, mock.Anything).Return(types.ErrNilEngine).Once()
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	engine.On("VirtualizationStop", mock.Anything, mock.Anything).Return(nil)
	// stop success
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerStop, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.NoError(t, r.Error)
	}
}

func TestControlRestart(t *testing.T) {
	c := NewTestCluster()
	ctx := context.Background()
	store := &storemocks.Store{}
	lock := &lockmocks.DistributedLock{}
	lock.On("Lock", mock.Anything).Return(nil)
	lock.On("Unlock", mock.Anything).Return(nil)
	c.store = store
	store.On("CreateLock", mock.Anything, mock.Anything).Return(lock, nil)
	engine := &enginemocks.API{}
	container := &types.Container{
		ID:         "id1",
		Privileged: true,
		Engine:     engine,
	}
	store.On("GetContainers", mock.Anything, mock.Anything).Return([]*types.Container{container}, nil)
	// failed, hook true, remove always false
	hook := &types.Hook{
		BeforeStop: []string{"cmd1"},
	}
	container.Hook = hook
	container.Hook.Force = true
	engine.On("ExecCreate", mock.Anything, mock.Anything, mock.Anything).Return("", types.ErrNilEngine)
	ch, err := c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerRestart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.Error(t, r.Error)
	}
	container.Hook = nil
	// success
	engine.On("VirtualizationStop", mock.Anything, mock.Anything).Return(nil)
	engine.On("VirtualizationStart", mock.Anything, mock.Anything).Return(nil)
	ch, err = c.ControlContainer(ctx, []string{"id1"}, cluster.ContainerRestart, false)
	assert.NoError(t, err)
	for r := range ch {
		assert.NoError(t, r.Error)
	}
}
