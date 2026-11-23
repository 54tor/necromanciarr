// Package crossseed reprend les réglages de cross-seed utiles à Necromanciarr, sans exécuter cross-seed.
package crossseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

const (
	maxSourceBytes    = 256 * 1024
	evaluationTimeout = 2 * time.Second
	// Un config.js de cross-seed n'appelle presque aucune fonction : 256 cadres suffisent largement.
	maxCallStackSize = 256
	scriptName       = "config.js"
)

// ErrEvaluationTimeout signale un config.js dont l'évaluation dépasse le délai accordé.
var ErrEvaluationTimeout = errors.New("cross-seed config: evaluation timed out")

// lockdownScript s'exécute avant le fichier de l'opérateur : il retire eval et tout constructeur de fonction
// (Function, function*, async function), seules portes vers une compilation de code à l'exécution.
const lockdownScript = `(function () {
	"use strict";
	var refuse = function () { throw new TypeError("code generation is disabled"); };
	var samples = [function () {}, function* () {}, async function () {}];
	for (var index = 0; index < samples.length; index++) {
		Object.defineProperty(Object.getPrototypeOf(samples[index]), "constructor", { value: refuse });
	}
	delete globalThis.eval;
	delete globalThis.Function;
})();`

// snapshotScript capture les primitives d'origine avant le fichier de l'opérateur, qui pourrait les remplacer ;
// la fonction rendue sérialise module.exports en JSON, ou rend null si ce n'est pas un objet simple.
const snapshotScript = `(function (getPrototypeOf, objectPrototype, isArray, stringify) {
	return function (value) {
		if (value === null || typeof value !== "object" || isArray(value)) { return null; }
		var prototype = getPrototypeOf(value);
		if (prototype !== objectPrototype && prototype !== null) { return null; }
		return stringify(value);
	};
})(Object.getPrototypeOf, Object.prototype, Array.isArray, JSON.stringify)`

// sandbox est un moteur neuf, sans E/S : seuls module, exports et process.env existent en plus du langage.
type sandbox struct {
	runtime  *goja.Runtime
	module   *goja.Object
	snapshot goja.Callable
}

// LoadConfigJS évalue le config.js de cross-seed dans un bac à sable et en extrait les clés reprises par Necromanciarr.
// process.env résout chaque nom par lookupEnv ; le script ne peut ni lire de fichier, ni charger de module, ni
// joindre le réseau. Les erreurs nomment la clé fautive, jamais une valeur.
func LoadConfigJS(ctx context.Context, path string, lookupEnv func(string) (string, bool)) (Imported, error) {
	source, err := readSource(path)
	if err != nil {
		return Imported{}, err
	}
	program, err := compileSource(source)
	if err != nil {
		return Imported{}, err
	}
	if err := ctx.Err(); err != nil {
		return Imported{}, fmt.Errorf("cross-seed config: %w", err)
	}
	box, err := newSandbox(lookupEnv)
	if err != nil {
		return Imported{}, err
	}
	stopInterrupts := box.armInterrupts(ctx)
	defer stopInterrupts()
	exportedJSON, err := box.evaluate(program)
	if err != nil {
		return Imported{}, err
	}
	return parseExports(exportedJSON)
}

func readSource(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- chemin choisi par l'opérateur (crossSeedConfig)
	if err != nil {
		return "", fmt.Errorf("cross-seed config: %w", err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return "", fmt.Errorf("cross-seed config: read: %w", err)
	}
	if len(content) > maxSourceBytes {
		return "", fmt.Errorf("cross-seed config: file too large (limit %d bytes)", maxSourceBytes)
	}
	return string(content), nil
}

// compileSource n'affiche que la position d'une erreur de syntaxe : le message du moteur peut citer le texte fautif.
func compileSource(source string) (*goja.Program, error) {
	ast, err := goja.Parse(scriptName, source, parser.WithDisableSourceMaps)
	if err != nil {
		var syntaxError *goja.CompilerSyntaxError
		if errors.As(err, &syntaxError) && syntaxError.File != nil {
			position := syntaxError.File.Position(syntaxError.Offset)
			return nil, fmt.Errorf("cross-seed config: syntax error at line %d, column %d", position.Line, position.Column)
		}
		return nil, errors.New("cross-seed config: syntax error")
	}
	program, err := goja.CompileAST(ast, false)
	if err != nil {
		return nil, errors.New("cross-seed config: compilation failed")
	}
	return program, nil
}

func newSandbox(lookupEnv func(string) (string, bool)) (*sandbox, error) {
	runtime := goja.New()
	runtime.SetMaxCallStackSize(maxCallStackSize)
	// eval est retiré, mais un code compilé à l'exécution lirait lui aussi le fichier nommé par sourceMappingURL.
	runtime.SetParserOptions(parser.WithDisableSourceMaps)
	if _, err := runtime.RunString(lockdownScript); err != nil {
		return nil, fmt.Errorf("cross-seed config: sandbox setup: %w", err)
	}
	snapshotValue, err := runtime.RunString(snapshotScript)
	if err != nil {
		return nil, fmt.Errorf("cross-seed config: sandbox setup: %w", err)
	}
	snapshot, isCallable := goja.AssertFunction(snapshotValue)
	if !isCallable {
		return nil, errors.New("cross-seed config: sandbox setup: snapshot is not callable")
	}
	module, err := exposeGlobals(runtime, lookupEnv)
	if err != nil {
		return nil, err
	}
	return &sandbox{runtime: runtime, module: module, snapshot: snapshot}, nil
}

// exposeGlobals définit module.exports non configurable : un accesseur posé dessus s'exécuterait côté Go, hors de
// portée de l'interruption.
func exposeGlobals(runtime *goja.Runtime, lookupEnv func(string) (string, bool)) (*goja.Object, error) {
	exports := runtime.NewObject()
	module := runtime.NewObject()
	if err := module.DefineDataProperty("exports", exports, goja.FLAG_TRUE, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
		return nil, fmt.Errorf("cross-seed config: sandbox setup: %w", err)
	}
	process := runtime.NewObject()
	environment := runtime.NewDynamicObject(environmentObject{runtime: runtime, lookupEnv: lookupEnv})
	if err := process.Set("env", environment); err != nil {
		return nil, fmt.Errorf("cross-seed config: sandbox setup: %w", err)
	}
	for name, value := range map[string]*goja.Object{"module": module, "exports": exports, "process": process} {
		if err := runtime.Set(name, value); err != nil {
			return nil, fmt.Errorf("cross-seed config: sandbox setup: %w", err)
		}
	}
	return module, nil
}

// armInterrupts arrête le script au délai ou à l'annulation du contexte ; la fonction rendue désarme les deux.
func (box *sandbox) armInterrupts(ctx context.Context) func() {
	timer := time.AfterFunc(evaluationTimeout, func() { box.runtime.Interrupt(ErrEvaluationTimeout) })
	stopContextWatch := context.AfterFunc(ctx, func() { box.runtime.Interrupt(ctx.Err()) })
	return func() {
		timer.Stop()
		stopContextWatch()
	}
}

// evaluate exécute le fichier puis sérialise module.exports dans le moteur : accesseurs et proxys s'y exécutent sous
// interruption, et le Go ne lit qu'une chaîne JSON.
func (box *sandbox) evaluate(program *goja.Program) (string, error) {
	if _, err := box.runtime.RunProgram(program); err != nil {
		return "", describeRunError(err)
	}
	snapshotJSON, err := box.snapshot(goja.Undefined(), box.module.Get("exports"))
	if err != nil {
		return "", describeRunError(err)
	}
	if goja.IsNull(snapshotJSON) || goja.IsUndefined(snapshotJSON) {
		return "", errors.New("cross-seed config: module.exports must be a plain object")
	}
	return snapshotJSON.String(), nil
}

// describeRunError ne reprend jamais le message d'une exception : le script peut y mettre une valeur de process.env.
func describeRunError(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if cause, isError := interrupted.Value().(error); isError {
			return fmt.Errorf("cross-seed config: evaluation interrupted: %w", cause)
		}
		return errors.New("cross-seed config: evaluation interrupted")
	}
	var stackOverflow *goja.StackOverflowError
	if errors.As(err, &stackOverflow) {
		return errors.New("cross-seed config: call stack limit exceeded")
	}
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return fmt.Errorf("cross-seed config: script raised an exception%s", exceptionLocation(exception))
	}
	return errors.New("cross-seed config: evaluation failed")
}

func exceptionLocation(exception *goja.Exception) string {
	for _, frame := range exception.Stack() {
		if position := frame.Position(); position.Line > 0 {
			return fmt.Sprintf(" at line %d", position.Line)
		}
	}
	return ""
}

// environmentObject est process.env : chaque nom lu passe par lookupEnv ; rien n'est énuméré ni écrit.
type environmentObject struct {
	runtime   *goja.Runtime
	lookupEnv func(string) (string, bool)
}

// Get rend la variable, ou nil (undefined) si elle n'est pas définie.
func (environment environmentObject) Get(name string) goja.Value {
	value, found := environment.lookupEnv(name)
	if !found {
		return nil
	}
	return environment.runtime.ToValue(value)
}

// Set refuse toute écriture.
func (environment environmentObject) Set(string, goja.Value) bool { return false }

// Has dit si la variable est définie.
func (environment environmentObject) Has(name string) bool {
	_, found := environment.lookupEnv(name)
	return found
}

// Delete refuse toute suppression.
func (environment environmentObject) Delete(string) bool { return false }

// Keys ne liste rien : un script ne doit pas pouvoir recopier tout l'environnement.
func (environment environmentObject) Keys() []string { return nil }
