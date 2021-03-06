package calcium

import (
	"context"
	"fmt"
	"sync"

	"github.com/projecteru2/core/cluster"
	enginetypes "github.com/projecteru2/core/engine/types"
	"github.com/projecteru2/core/metrics"
	"github.com/projecteru2/core/store"
	"github.com/projecteru2/core/types"
	"github.com/projecteru2/core/utils"
	"github.com/sanity-io/litter"
	log "github.com/sirupsen/logrus"
)

// CreateContainer use options to create containers
func (c *Calcium) CreateContainer(ctx context.Context, opts *types.DeployOptions) (chan *types.CreateContainerMessage, error) {
	opts.Normalize()
	opts.ProcessIdent = utils.RandomString(16)
	log.Infof("[CreateContainer %s] Creating container with options:", opts.ProcessIdent)
	litter.Dump(opts)
	// Count 要大于0
	if opts.Count <= 0 {
		return nil, types.NewDetailedErr(types.ErrBadCount, opts.Count)
	}
	// 创建时内存不为 0
	if opts.Memory < 0 {
		return nil, types.NewDetailedErr(types.ErrBadMemory, opts.Memory)
	}
	// CPUQuota 也需要大于 0
	if opts.CPUQuota < 0 {
		return nil, types.NewDetailedErr(types.ErrBadCPU, opts.CPUQuota)
	}
	return c.doCreateContainer(ctx, opts)
}

func (c *Calcium) doCreateContainer(ctx context.Context, opts *types.DeployOptions) (chan *types.CreateContainerMessage, error) {
	ch := make(chan *types.CreateContainerMessage)
	// RFC 计算当前 app 部署情况的时候需要保证同一时间只有这个 app 的这个 entrypoint 在跑
	// 因此需要在这里加个全局锁，直到部署完毕才释放
	// 通过 Processing 状态跟踪达成 18 Oct, 2018
	nodesInfo, err := c.doAllocResource(ctx, opts)
	if err != nil {
		log.Errorf("[doCreateContainer] Error during alloc resource: %v", err)
		return ch, err
	}

	go func() {
		defer close(ch)
		wg := sync.WaitGroup{}
		wg.Add(len(nodesInfo))
		index := 0

		// do deployment by each node
		for _, nodeInfo := range nodesInfo {
			go metrics.Client.SendDeployCount(nodeInfo.Deploy)
			go func(nodeInfo types.NodeInfo, index int) {
				_ = utils.Txn(
					ctx,
					func(ctx context.Context) error {
						for i, m := range c.doCreateContainerOnNode(ctx, nodeInfo, opts, index) {
							_ = utils.Txn(
								ctx,
								func(ctx context.Context) error {
									ch <- m // nolint
									return nil
								},
								func(ctx context.Context) error {
									// decr processing count
									if err := c.store.UpdateProcessing(ctx, opts, nodeInfo.Name, nodeInfo.Deploy-i-1); err != nil { // nolint
										log.Warnf("[doCreateContainer] Update processing count failed %v", err)
									}
									return nil
								},
								nil,
								c.config.GlobalTimeout,
							)
						}
						return nil
					},
					func(ctx context.Context) error {
						if err := c.store.DeleteProcessing(ctx, opts, nodeInfo); err != nil {
							log.Errorf("[doCreateContainer] remove processing status failed %v", err)
						}
						wg.Done()
						return nil
					},
					nil,
					c.config.GlobalTimeout,
				)
			}(nodeInfo, index)
			index += nodeInfo.Deploy
		}
		wg.Wait()
	}()

	return ch, nil
}

func (c *Calcium) doCreateContainerOnNode(ctx context.Context, nodeInfo types.NodeInfo, opts *types.DeployOptions, index int) []*types.CreateContainerMessage {
	ms := make([]*types.CreateContainerMessage, nodeInfo.Deploy)
	for i := 0; i < nodeInfo.Deploy; i++ {
		// createAndStartContainer will auto cleanup
		cpu := types.CPUMap{}
		if len(nodeInfo.CPUPlan) > 0 {
			cpu = nodeInfo.CPUPlan[i]
		}
		volumePlan := types.VolumePlan{}
		if len(nodeInfo.VolumePlans) > 0 {
			volumePlan = nodeInfo.VolumePlans[i]
		}

		node := &types.Node{}
		if err := utils.Txn(
			ctx,
			// if
			func(ctx context.Context) (err error) {
				node, err = c.doGetAndPrepareNode(ctx, nodeInfo.Name, opts.Image)
				ms[i] = &types.CreateContainerMessage{ // nolint
					Error:      err,
					CPU:        cpu,
					VolumePlan: volumePlan,
				}
				return
			},
			// then
			func(ctx context.Context) error {
				ms[i] = c.doCreateAndStartContainer(ctx, i+index, node, opts, cpu, volumePlan) // nolint
				return ms[i].Error                                                             // nolint
			},
			// rollback, will use background context
			func(ctx context.Context) (err error) {
				log.Errorf("[doCreateContainerOnNode] Error when create and start a container, %v", ms[i].Error) // nolint
				if ms[i].ContainerID != "" {                                                                     // nolint
					log.Warnf("[doCreateContainer] Create container failed %v, and container %s not removed", ms[i].Error, ms[i].ContainerID) // nolint
					return
				}
				if err = c.withNodeLocked(ctx, nodeInfo.Name, func(node *types.Node) error {
					return c.store.UpdateNodeResource(ctx, node, cpu, opts.CPUQuota, opts.Memory, opts.Storage, volumePlan.IntoVolumeMap(), store.ActionIncr)
				}); err != nil {
					log.Errorf("[doCreateContainer] Reset node resource %s failed %v", nodeInfo.Name, err)
				}
				return
			},
			c.config.GlobalTimeout,
		); err != nil {
			continue
		}
		log.Infof("[doCreateContainerOnNode] create container success %s", ms[i].ContainerID)
	}

	return ms
}

func (c *Calcium) doGetAndPrepareNode(ctx context.Context, nodename, image string) (*types.Node, error) {
	node, err := c.GetNode(ctx, nodename)
	if err != nil {
		return nil, err
	}

	return node, pullImage(ctx, node, image)
}

func (c *Calcium) doCreateAndStartContainer(
	ctx context.Context,
	no int, node *types.Node,
	opts *types.DeployOptions,
	cpu types.CPUMap,
	volumePlan types.VolumePlan,
) *types.CreateContainerMessage {
	container := &types.Container{
		Podname:    opts.Podname,
		Nodename:   node.Name,
		CPU:        cpu,
		Quota:      opts.CPUQuota,
		Memory:     opts.Memory,
		Storage:    opts.Storage,
		Hook:       opts.Entrypoint.Hook,
		Privileged: opts.Entrypoint.Privileged,
		Engine:     node.Engine,
		SoftLimit:  opts.SoftLimit,
		Image:      opts.Image,
		Env:        opts.Env,
		User:       opts.User,
		Volumes:    opts.Volumes,
		VolumePlan: volumePlan,
	}
	createContainerMessage := &types.CreateContainerMessage{
		Podname:    container.Podname,
		Nodename:   container.Nodename,
		CPU:        cpu,
		Quota:      opts.CPUQuota,
		Memory:     opts.Memory,
		Storage:    opts.Storage,
		VolumePlan: volumePlan,
		Publish:    map[string][]string{},
	}
	var err error
	var containerCreated *enginetypes.VirtualizationCreated

	_ = utils.Txn(
		ctx,
		func(ctx context.Context) error {
			// get config
			config := c.doMakeContainerOptions(no, cpu, volumePlan, opts, node)
			container.Name = config.Name
			container.Labels = config.Labels
			createContainerMessage.ContainerName = container.Name

			// create container
			containerCreated, err = node.Engine.VirtualizationCreate(ctx, config)
			if err != nil {
				return err
			}
			container.ID = containerCreated.ID

			// Copy data to container
			if len(opts.Data) > 0 {
				for dst, readerManager := range opts.Data {
					reader, err := readerManager.GetReader()
					if err != nil {
						return err
					}
					if err = c.doSendFileToContainer(ctx, node.Engine, container.ID, dst, reader, true, true); err != nil {
						return err
					}
				}
			}

			// deal with hook
			if len(opts.AfterCreate) > 0 && container.Hook != nil {
				container.Hook = &types.Hook{
					AfterStart: append(opts.AfterCreate, container.Hook.AfterStart...),
					Force:      container.Hook.Force,
				}
			}

			// start first
			createContainerMessage.Hook, err = c.doStartContainer(ctx, container, opts.IgnoreHook)
			if err != nil {
				return err
			}

			// inspect real meta
			var containerInfo *enginetypes.VirtualizationInfo
			containerInfo, err = container.Inspect(ctx) // 补充静态元数据
			if err != nil {
				return err
			}

			// update meta
			if containerInfo.Networks != nil {
				createContainerMessage.Publish = utils.MakePublishInfo(containerInfo.Networks, opts.Entrypoint.Publish)
			}
			// reset users
			if containerInfo.User != container.User {
				container.User = containerInfo.User
			}
			// reset container.hook
			container.Hook = opts.Entrypoint.Hook
			return nil
		},
		func(ctx context.Context) error {
			// store eru container
			if err = c.store.AddContainer(ctx, container); err != nil {
				return err
			}
			// non-empty message.ContainerID means "core saves metadata of this container"
			createContainerMessage.ContainerID = container.ID
			return nil
		},
		func(ctx context.Context) error {
			createContainerMessage.Error = err
			if err != nil && container.ID != "" {
				if err := c.doRemoveContainer(ctx, container, true); err != nil {
					log.Errorf("[doCreateAndStartContainer] create and start container failed, and remove it failed also, %s, %v", container.ID, err)
					return err
				}
				createContainerMessage.ContainerID = ""
			}
			return nil
		},
		c.config.GlobalTimeout,
	)
	return createContainerMessage
}

func (c *Calcium) doMakeContainerOptions(index int, cpumap types.CPUMap, volumePlan types.VolumePlan, opts *types.DeployOptions, node *types.Node) *enginetypes.VirtualizationCreateOptions {
	config := &enginetypes.VirtualizationCreateOptions{}
	// general
	config.Seq = index
	config.CPU = cpumap
	config.Quota = opts.CPUQuota
	config.Memory = opts.Memory
	config.Storage = opts.Storage
	config.NUMANode = node.GetNUMANode(cpumap)
	config.SoftLimit = opts.SoftLimit
	config.RawArgs = opts.RawArgs
	config.Lambda = opts.Lambda
	config.User = opts.User
	config.DNS = opts.DNS
	config.Image = opts.Image
	config.Stdin = opts.OpenStdin
	config.Hosts = opts.ExtraHosts
	config.Volumes = opts.Volumes.ApplyPlan(volumePlan).ToStringSlice(false, true)
	config.VolumePlan = volumePlan.ToLiteral()
	config.Debug = opts.Debug
	config.Network = opts.NetworkMode
	config.Networks = opts.Networks

	// entry
	entry := opts.Entrypoint
	config.WorkingDir = entry.Dir
	config.Privileged = entry.Privileged
	config.RestartPolicy = entry.RestartPolicy
	config.Sysctl = entry.Sysctls
	config.Publish = entry.Publish
	if entry.Log != nil {
		config.LogType = entry.Log.Type
		config.LogConfig = entry.Log.Config
	}
	// name
	suffix := utils.RandomString(6)
	config.Name = utils.MakeContainerName(opts.Name, opts.Entrypoint.Name, suffix)
	// command and user
	// extra args is dynamically
	slices := utils.MakeCommandLineArgs(fmt.Sprintf("%s %s", entry.Command, opts.ExtraArgs))
	config.Cmd = slices
	// env
	env := append(opts.Env, fmt.Sprintf("APP_NAME=%s", opts.Name))
	env = append(env, fmt.Sprintf("ERU_POD=%s", opts.Podname))
	env = append(env, fmt.Sprintf("ERU_NODE_NAME=%s", node.Name))
	env = append(env, fmt.Sprintf("ERU_CONTAINER_NO=%d", index))
	env = append(env, fmt.Sprintf("ERU_MEMORY=%d", opts.Memory))
	env = append(env, fmt.Sprintf("ERU_STORAGE=%d", opts.Storage))
	config.Env = env
	// basic labels, bind to LabelMeta
	config.Labels = map[string]string{
		cluster.ERUMark: "1",
		cluster.LabelMeta: utils.EncodeMetaInLabel(&types.LabelMeta{
			Publish:     opts.Entrypoint.Publish,
			HealthCheck: entry.HealthCheck,
		}),
	}
	for key, value := range opts.Labels {
		config.Labels[key] = value
	}

	return config
}
