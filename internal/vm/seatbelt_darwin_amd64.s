//go:build darwin

#include "textflag.h"

TEXT libc_sandbox_init_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_sandbox_init(SB)
GLOBL	·libc_sandbox_init_trampoline_addr(SB), RODATA, $8
DATA	·libc_sandbox_init_trampoline_addr(SB)/8, $libc_sandbox_init_trampoline<>(SB)

TEXT libc_sandbox_free_error_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_sandbox_free_error(SB)
GLOBL	·libc_sandbox_free_error_trampoline_addr(SB), RODATA, $8
DATA	·libc_sandbox_free_error_trampoline_addr(SB)/8, $libc_sandbox_free_error_trampoline<>(SB)
