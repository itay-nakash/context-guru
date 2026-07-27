"""Claude Code + rtk (Rust Token Killer) agent.

Identical to the stock ``claude-code`` agent, except it makes the ``rtk`` CLI
proxy active inside the task container so that **bash tool output is compressed
at the shell** before it ever enters the model context.

rtk is architecturally different from request-stream compaction proxies
(context-guru, headroom): it is a Claude Code ``PreToolUse`` hook that rewrites
Bash commands (``pytest ...`` -> ``rtk pytest ...``, ``cat f`` -> ``rtk read f``,
``git status`` -> ``rtk git status``) *inside the container*. It is not on the
network path and needs no proxy. Model routing is therefore whatever the harness
already points ``ANTHROPIC_BASE_URL`` at — for an apples-to-apples comparison the
harness routes it exactly like the ``off`` baseline, so the ONLY difference from
baseline is the in-container bash compression.

This subclass:
  1. ``install()`` — after the stock Claude Code install, uploads a host-built
     static-musl ``rtk`` binary to ``/usr/local/bin/rtk`` (path from
     ``RTK_BIN_HOST``, default ``/tmp/rtk-runs/rtk``) and also symlinks it into
     ``~/.local/bin`` so it is on the same PATH Claude Code launches with.
  2. ``run()`` — installs the rtk ``PreToolUse`` hook into the exact
     ``CLAUDE_CONFIG_DIR`` Claude Code will read (``rtk init -g --auto-patch``,
     which honors ``CLAUDE_CONFIG_DIR``), runs the stock agent, then dumps
     ``rtk gain --all --format json`` to ``/logs/agent/rtk-gain.json`` so the
     harness can report rtk's own bash-output savings per trial.

End-to-end trajectory metrics (reward, steps, cache-read/write, billed cost,
cache-hit, wall) are produced by the unchanged parent class, so they are
measured identically to the other benchmark arms.
"""

import os
from typing import override

from harbor.agents.installed.claude_code import ClaudeCode
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.agent.name import AgentName
from harbor.models.trial.paths import EnvironmentPaths

# Host path to the static-musl rtk binary uploaded into each container.
RTK_BIN_HOST = os.environ.get("RTK_BIN_HOST", "/tmp/rtk-runs/rtk")


class ClaudeCodeRTK(ClaudeCode):
    @staticmethod
    @override
    def name() -> str:
        return AgentName.CLAUDE_CODE_RTK.value

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        # Stock Claude Code install first (curl/npm bootstrap of the CLI).
        await super().install(environment)

        # Deliver the rtk binary (static-musl, runs on any glibc/musl image).
        await environment.upload_file(RTK_BIN_HOST, "/usr/local/bin/rtk")
        await self.exec_as_root(
            environment,
            command="chmod 755 /usr/local/bin/rtk && /usr/local/bin/rtk --version",
        )
        # Belt-and-suspenders: Claude Code launches with PATH prepending
        # ~/.local/bin, and both the PreToolUse hook (`rtk hook claude`) and the
        # rewritten commands (`rtk pytest`) must resolve `rtk`. Symlink it there
        # too so it is found regardless of whether /usr/local/bin is on PATH.
        await self.exec_as_agent(
            environment,
            command='mkdir -p "$HOME/.local/bin" && ln -sf /usr/local/bin/rtk "$HOME/.local/bin/rtk"',
        )

    @override
    async def run(
        self, instruction: str, environment: BaseEnvironment, context: AgentContext
    ) -> None:
        # Install the rtk PreToolUse hook into the SAME config dir Claude Code
        # will read (claude_code.py sets CLAUDE_CONFIG_DIR to this path). rtk
        # init honors CLAUDE_CONFIG_DIR; --auto-patch is REQUIRED for headless
        # (piped stdin otherwise defaults the settings-patch to "N").
        config_dir = (EnvironmentPaths.agent_dir / "sessions").as_posix()
        await self.exec_as_agent(
            environment,
            command=(
                'export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"; '
                "mkdir -p /logs/agent "
                f'"{config_dir}"; '
                f'CLAUDE_CONFIG_DIR="{config_dir}" '
                "rtk init -g --auto-patch "
                "> /logs/agent/rtk-init.log 2>&1 || true; "
                # Snapshot the installed hook so we can confirm it fired.
                f'cp "{config_dir}/settings.json" /logs/agent/rtk-settings.json '
                "2>/dev/null || true"
            ),
            env={"RTK_TELEMETRY_DISABLED": "1"},
        )

        # Run the stock Claude Code agent (its own setup_command only mkdir's
        # under CLAUDE_CONFIG_DIR, so it does not clobber settings.json).
        await super().run(instruction, environment, context)

        # Dump rtk's own savings ledger for this trial. The rewritten bash-tool
        # commands ran as the agent user and tracked to the default per-user DB
        # (~/.local/share/rtk/history.db); read it back the same way.
        await self.exec_as_agent(
            environment,
            command=(
                'export PATH="/usr/local/bin:$HOME/.local/bin:$PATH"; '
                "rtk gain --all --format json > /logs/agent/rtk-gain.json "
                "2>/dev/null || true; "
                "rtk gain --history > /logs/agent/rtk-history.txt 2>/dev/null || true"
            ),
            env={"RTK_TELEMETRY_DISABLED": "1"},
        )
