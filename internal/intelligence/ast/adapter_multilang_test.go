//go:build cgo

package ast

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// adapterFixture is one bounded syntax shape per registered language. The
// conformance assertions are canonical facts, not a claim of full grammar
// coverage.
type adapterFixture struct {
	path        string
	body        string
	language    Language
	wantSymbols []string
	wantImports bool
	wantTests   bool
	wantEntry   bool
}

func adapterFixtures() []adapterFixture {
	return []adapterFixture{
		{
			path: "sample.tsx", language: LanguageTSX,
			body: `import React from "react";
export interface Props { value: number }
export class Widget implements Props { value = 1; }
export default function App() { return <div><Widget/></div>; }
`,
			wantSymbols: []string{"Props", "Widget", "App"}, wantImports: true,
		},
		{
			path: "sample.jsx", language: LanguageJavaScript,
			body: `import { value } from "./value";
export function App() { return <div>{value()}</div>; }
`,
			wantSymbols: []string{"App"}, wantImports: true,
		},
		{
			path: "Sample.java", language: LanguageJava,
			body: `package com.example;
import java.util.List;
public class Sample extends Base implements Runnable {
  private int value;
  public void run() { helper(value); }
  static int helper(int x) { return x; }
  public static void main(String[] args) {}
}
interface Base {}
`,
			wantSymbols: []string{"Sample", "run", "helper", "main", "Base"},
			wantImports: true, wantEntry: true,
		},
		{
			path: "Sample.cs", language: LanguageCSharp,
			body: `using System;
namespace Example;
public interface IWorker { void Run(); }
public class Sample : IWorker {
  public void Run() { Helper(); }
  static int Helper() { return 1; }
  public static void Main() {}
}
`,
			wantSymbols: []string{"IWorker", "Sample", "Run", "Helper", "Main"},
			wantImports: true, wantEntry: true,
		},
		{
			path: "sample.rs", language: LanguageRust,
			body: `use std::fmt;
pub trait Worker { fn run(&self); }
pub struct Sample { value: i32 }
impl Worker for Sample { fn run(&self) { helper(); } }
pub fn helper() -> i32 { 1 }
#[test]
fn it_works() { helper(); }
fn main() { helper(); }
`,
			wantSymbols: []string{"Worker", "Sample", "run", "helper", "it_works", "main"},
			wantImports: true, wantTests: true, wantEntry: true,
		},
		{
			path: "Sample.kt", language: LanguageKotlin,
			body: `package example
import java.util.List
interface Worker { fun run(): Int }
data class Point(val x: Int)
class Sample : Worker { override fun run(): Int = helper() }
fun helper(): Int = 1
fun main() { helper() }
`,
			wantSymbols: []string{"Worker", "Point", "Sample", "run", "helper", "main"},
			wantImports: true, wantEntry: true,
		},
		{
			path: "sample.rb", language: LanguageRuby,
			body: `require "json"
module Example
  class Sample
    def run
      helper(1)
    end
  end
  def helper(x)
    x
  end
end
`,
			wantSymbols: []string{"Sample", "run", "helper"}, wantImports: true,
		},
		{
			path: "sample.c", language: LanguageC,
			body: `#include <stdio.h>
struct Point { int x; };
int helper(int x) { return x; }
int main(void) { return helper(1); }
`,
			wantSymbols: []string{"Point", "helper", "main"}, wantImports: true, wantEntry: true,
		},
		{
			path: "sample.cpp", language: LanguageCpp,
			body: `#include "local.hpp"
namespace app {
class Base {};
class Sample : public Base { public: int run() { return helper(1); } };
int helper(int x) { return x; }
int main() { return 0; }
}
`,
			wantSymbols: []string{"Base", "Sample", "run", "helper", "main"},
			wantImports: true, wantEntry: true,
		},
	}
}

func TestLanguageAdaptersEmitCanonicalFacts(t *testing.T) {
	set, err := NewAdapterSet(DefaultAdapters()...)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, fixture := range adapterFixtures() {
		t.Run(string(fixture.language), func(t *testing.T) {
			path := filepath.Join(root, fixture.path)
			if err := os.WriteFile(path, []byte(fixture.body), 0o644); err != nil {
				t.Fatal(err)
			}
			adapter, ok := set.AdapterFor(path)
			if !ok {
				t.Fatalf("no adapter for %s", fixture.path)
			}
			facts, err := adapter.ParseFile(path, ParseOptions{Relationships: true})
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if facts.Language != fixture.language {
				t.Fatalf("language = %s, want %s", facts.Language, fixture.language)
			}
			if facts.Adapter == "" || facts.Parser.Name == "" {
				t.Fatalf("adapter provenance missing: %+v", facts)
			}
			names := map[string]bool{}
			for _, symbol := range facts.Symbols {
				if symbol.Path != path {
					t.Errorf("%s path = %q, want %q", symbol.Name, symbol.Path, path)
				}
				if symbol.Line < 1 || symbol.EndLine < symbol.Line {
					t.Errorf("%s coordinate = %d..%d", symbol.Name, symbol.Line, symbol.EndLine)
				}
				if symbol.Language != fixture.language {
					t.Errorf("%s language = %s", symbol.Name, symbol.Language)
				}
				if symbol.Canonical == "" {
					t.Errorf("%s has no canonical kind", symbol.Name)
				}
				names[symbol.Name] = true
				names[symbol.CanonicalString()] = true
			}
			for _, want := range fixture.wantSymbols {
				if !names[want] && !strings.Contains(strings.Join(mapKeys(names), ","), want) {
					t.Errorf("symbols = %v, missing %q", mapKeys(names), want)
				}
			}
			if fixture.wantImports && len(facts.Imports) == 0 {
				t.Error("imports were not extracted")
			}
			if fixture.wantTests && len(facts.Tests) == 0 {
				t.Error("tests were not extracted")
			}
			if fixture.wantEntry && len(facts.Entrypoints) == 0 {
				t.Error("entrypoint was not extracted")
			}
			if facts.Partial {
				t.Errorf("valid fixture reported partial: %+v", facts.Issues)
			}
			second, err := adapter.ParseFile(path, ParseOptions{Relationships: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(symbolNames(facts.Symbols), symbolNames(second.Symbols)) {
				t.Errorf("adapter output is not deterministic:\n%v\n%v",
					symbolNames(facts.Symbols), symbolNames(second.Symbols))
			}
		})
	}
}

func TestAdapterRegistryRejectsAmbiguousOwnership(t *testing.T) {
	first := &testAdapter{name: "first", extensions: []string{".x"}}
	second := &testAdapter{name: "second", extensions: []string{"x"}}
	if _, err := NewAdapterSet(first, second); err == nil {
		t.Fatal("ambiguous extension ownership was accepted")
	}
}

func TestAdapterRegistryKeepsGoNative(t *testing.T) {
	set, err := NewAdapterSet(DefaultAdapters()...)
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := set.AdapterFor("main.go")
	if !ok || adapter.Name() != "go" {
		t.Fatalf("Go adapter = %v/%v, want the native backend", adapter, ok)
	}
}

func TestRecoverableSyntaxErrorIsPartialNotEmpty(t *testing.T) {
	set, err := NewAdapterSet(DefaultAdapters()...)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "broken.ts")
	if err := os.WriteFile(path, []byte("export class Kept { method() { return 1; } }\nconst = ;"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter, _ := set.AdapterFor(path)
	facts, err := adapter.ParseFile(path, ParseOptions{})
	if err != nil {
		t.Fatalf("recoverable parse returned a fatal error: %v", err)
	}
	if !facts.Partial || len(facts.Issues) == 0 {
		t.Fatalf("syntax error was not disclosed: partial=%v issues=%+v", facts.Partial, facts.Issues)
	}
	if len(facts.Symbols) == 0 {
		t.Fatal("recoverable tree lost the declaration it did parse")
	}
}

// CanonicalString keeps the test's failure output readable without exposing
// the raw grammar node type as a contract.
func (m Match) CanonicalString() string { return string(m.Canonical) }

func symbolNames(symbols []Match) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, symbol.Name+":"+string(symbol.Canonical))
	}
	return out
}

func mapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

// testAdapter is a minimal duplicate-ownership probe.
type testAdapter struct {
	name       string
	extensions []string
}

func (a *testAdapter) Name() string               { return a.name }
func (a *testAdapter) Language() Language         { return "test" }
func (a *testAdapter) Languages() []Language      { return []Language{"test"} }
func (a *testAdapter) Extensions() []string       { return a.extensions }
func (a *testAdapter) Supported(path string) bool { return true }
func (a *testAdapter) Capabilities() Capabilities { return Capabilities{} }
func (a *testAdapter) ParseFile(string, ParseOptions) (FileFacts, error) {
	return FileFacts{}, nil
}
