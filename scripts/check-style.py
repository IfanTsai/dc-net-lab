#!/usr/bin/env python3
"""检查 docs/golang-style.md 中 golangci-lint 覆盖不到的空行语义规则。

规则(违规则退出码 1 并逐条列出):
  1. 完整的 if/for/switch 块结束后应有一个空行(任意嵌套层级)。
     例外:块后紧跟收尾括号、case/default 标签。
  2. 函数末尾的最终 return 前应有一个空行。
     例外:上一行是块/函数开头 "{"、case/default 标签(单语句体保持紧凑);
     return 上方紧贴的注释视为 return 的一部分,空行要求移到注释组之前。
  3. 不允许连续两个及以上空行。

扫描 internal/、controller/、agent/、nodeapps/ 与 web/server/ 下的手写 Go 文件,排除 pb/ 与 wire_gen.go。
"""

import glob
import re
import sys

RET_PAT = re.compile(r"^(\t+)return\b")
CLOSE_PAT = re.compile(r"^(\t+)\}$")
CASE_PAT = re.compile(r"^(case .*|default):$")


def go_files():
    files = (
        glob.glob("internal/**/*.go", recursive=True)
        + glob.glob("controller/**/*.go", recursive=True)
        + glob.glob("agent/**/*.go", recursive=True)
        + glob.glob("nodeapps/**/*.go", recursive=True)
        + glob.glob("web/server/**/*.go", recursive=True)
    )

    return [f for f in files if "/pb/" not in f and "wire_gen" not in f]


def is_comment(line):
    return line.lstrip("\t").startswith("//")


def check_file(path):
    violations = []
    lines = open(path).read().split("\n")

    for i in range(1, len(lines)):
        line, prev = lines[i], lines[i - 1]

        # 规则3:连续空行
        if i >= 2 and line == "" and prev == "":
            violations.append((i + 1, "连续空行"))

        # 规则1:块结束后缺空行
        m = CLOSE_PAT.match(prev)
        if m and line.startswith(m.group(1)):
            rest = line[len(m.group(1)):]
            if rest and rest[0] not in "})" and not rest.startswith(("case ", "default:")):
                violations.append((i + 1, "if/for/switch 块结束后缺空行"))

        # 规则2:return 前缺空行(注释吸附到 return)
        m = RET_PAT.match(line)
        if not m:
            continue

        j = i - 1
        while j >= 0 and is_comment(lines[j]) and lines[j].startswith(m.group(1)):
            j -= 1
        if j < 0:
            continue

        stripped = lines[j].strip()
        if (
            stripped
            and not stripped.endswith("{")
            and not CASE_PAT.match(stripped)
        ):
            violations.append((i + 1, "return 前缺空行"))

    return violations


def main():
    failed = False
    for f in sorted(go_files()):
        for lineno, msg in check_file(f):
            print(f"{f}:{lineno}: {msg}")
            failed = True

    if failed:
        print("\nstyle check failed; 规则见 docs/golang-style.md 第 2/5 章", file=sys.stderr)

        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
