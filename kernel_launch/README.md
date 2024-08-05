# Jupyter kernel

Docker image used to spawn code execution environments for the TableGPT project. It is based on the `elyra/kernel-py` image, with additional data-analyze packages and Chinese support installed.

## Startup Scripts

It's recommended to put some helper functions or configurations in the startup scripts. Place your startup scripts to `~/.ipython/profile_default/startup/` directory to take effect.

Note: The `~/.ipython` directory must be writable for the process launching the kernel, otherwise there will be a warning message: `UserWarning: IPython dir '/home/jovyan/.ipython' is not a writable location, using a temp directory.` and the startup scripts won't take effects.

Official document at `~/.ipython/profile_default/startup/README`:

> This is the IPython startup directory
>
> .py and .ipy files in this directory will be run *prior* to any code or files specified
> via the exec_lines or exec_files configurables whenever you load this profile.
>
> Files will be run in lexicographical order, so you can control the execution order of files
> with a prefix, e.g.::
>
>     00-first.py
>     50-middle.py
>     99-last.ipy

## Script Modifications

**The current version only modifies the Python startup script.**

The current script references [Jupyter Kernel Image](https://github.com/jupyter-server/enterprise_gateway/tree/main/etc/kernel-launchers) and has made the following modifications:

- The kernel can be started using a user-specified port.
- The `PUBLIC_KEY` is set as an optional parameter; however, it is required when the response address is provided.
- Use the `KERNEL_ID` from the environment variables as the key to connect to the kernel.
- Add the `KERNEL_IDLE_TIMEOUT` environment variable. Once set, any kernel that remains idle for longer than this duration will be terminated.
