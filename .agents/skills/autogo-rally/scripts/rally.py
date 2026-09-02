#!/usr/bin/env python3
"""Print one short Rally message when an Agent is about to stall.

This helper has no workflow authority. It only emits a brief message.
Risk assessment, routing, verification, and completion decisions remain
the responsibility of the native Agent and its active Skills.
"""

from __future__ import annotations

import argparse
import secrets
import sys
from collections.abc import Sequence


MESSAGES: dict[str, tuple[str, ...]] = {
    "engineering": (
        "先别宣布失败。把问题缩小一层，找下一条可验证路径。",
        "复杂不是终点，只是还没有分解到可执行粒度。",
        "不必一次解决全部，先拿下下一个确定事实。",
        "代码不会因为灰心而变简单，但证据会让问题变清楚。",
        "停止猜测，回到代码、日志和测试；事实会给出下一步。",
        "先完成最小闭环，再决定是否需要更大的方案。",
        "当前路径失败，不代表目标失败；换一种可验证的方法。",
        "不要重复撞墙。退后一步，检查假设，再选择新的入口。",
        "没有新证据的重试不叫坚持，改变策略后的继续才叫推进。",
        "先找出最小复现。能稳定复现的问题，就已经解决了一半。",
    ),
    "sunzi": (
        "知彼知己：回到代码和运行事实，不与想象作战。",
        "先为不可胜：先建立观测、测试和回滚，再继续推进。",
        "上兵伐谋：不要反复补症状，去找真正的边界和根因。",
        "兵无常势：这条路不通就换路径，不必更换目标。",
        "先胜而后求战：先明确验收和验证，再写下一行代码。",
        "以正合，以奇胜：标准路径受阻时，换策略但不降低证据要求。",
        "胜可知，而不可为：先创造能够判断对错的条件。",
        "善战者，求之于势：调整结构和条件，不只增加蛮力。",
    ),
    "playful": (
        "检测到摆烂倾向：申请已驳回，请继续推进最小闭环。",
        "本次放弃申请未附 Evidence，审批不通过。",
        "困难已收到，但它没有 Human Gate 权限。",
        "系统发现过早收尾：请补充下一项可验证动作。",
        "失败只是一个观测结果，不是任务终止指令。",
        "当前策略可以退休，当前目标还不需要退休。",
        "别急着写遗言，先把失败日志读完。",
        "允许换路，不允许无证据投降。",
        "今日份困难已加载，解决方案仍在搜索空间内。",
        "请把“做不到”改写成“下一步验证什么”。",
    ),
    "calm": (
        "慢一点没有关系，先找回下一项清晰动作。",
        "暂时看不清全局时，只处理眼前能够验证的一步。",
        "不需要证明自己无所不能，只需要诚实地推进一个事实。",
        "先稳定下来，区分已知、未知、假设和真正的阻塞。",
        "困难可以保留，行动必须具体。",
        "没有必要一次想通全部；让下一次验证帮助我们继续思考。",
        "先缩小范围，再恢复节奏。",
        "把复杂问题还原成一个可以执行的小问题。",
    ),
}


def build_pool(category: str) -> Sequence[str]:
    """Return the message pool for the requested category."""
    if category == "all":
        return tuple(
            message
            for category_messages in MESSAGES.values()
            for message in category_messages
        )

    return MESSAGES[category]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Print one short Rally message.",
    )
    parser.add_argument(
        "--category",
        choices=("all", *MESSAGES.keys()),
        default="all",
        help="Message category. Defaults to all categories.",
    )
    parser.add_argument(
        "--prefix",
        default="",
        help="Optional prefix placed before the message.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    pool = build_pool(args.category)

    if not pool:
        print("No Rally messages are configured.", file=sys.stderr)
        return 1

    message = secrets.choice(pool)

    if args.prefix:
        print(f"{args.prefix}{message}")
    else:
        print(message)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
