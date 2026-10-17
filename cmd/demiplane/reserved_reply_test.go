// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build reply

package main

// moduleRouteNames are the route names of the reply module, which owns
// /reply/{slug} and /replies on the control plane and /answer/{slug} on the
// content origin.
var moduleRouteNames = []string{"reply", "replies", "answer"}
