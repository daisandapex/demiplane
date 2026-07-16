// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package store

// Embedded wordlists for friendly slug generation (adjective + single-word
// creature), dependency-free (no petname library). All entries are lowercase
// [a-z] only and contain no hyphens, so the "adjective-creature" join is
// unambiguous.
//
// Keyspace = len(adjectives) * len(creatures) (~5.7k unsuffixed pairs) — ample
// for a trusted-network default while staying memorable (e.g. shadow-specter,
// radiant-owlbear). When that space gets crowded the generator appends a numeric
// suffix. See slug.go for the entropy caveat.
//
// Attribution: D&D-themed wordlist generated from the D&D 5e API
// (https://www.dnd5eapi.co), the open-source 5e-bits project. Creature and term
// names are D&D 5e System Reference Document content, (c) Wizards of the Coast,
// used under the OGL 1.0a / CC-BY-4.0.

var adjectives = []string{
	"abjuration", "abyssal", "acid", "arcane", "astral", "celestial", "charmed",
	"cold", "conjuration", "divination", "draconic", "eldritch", "enchantment",
	"ethereal", "evocation", "fey", "feywild", "fiendish", "fire", "force",
	"frightened", "gilded", "illusion", "infernal", "invisible", "lightning",
	"necromancy", "necrotic", "obsidian", "paralyzed", "petrified", "planar",
	"poison", "poisoned", "psychic", "radiant", "runic", "shadow", "spectral",
	"stunned", "sundered", "thunder", "transmutation", "umbral", "warded",
}

var creatures = []string{
	"aboleth", "androsphinx", "ankheg", "azer", "baboon", "badger", "balor",
	"basilisk", "behir", "boar", "bugbear", "bulette", "camel", "centaur",
	"chimera", "chuul", "cloaker", "cockatrice", "couatl", "crab", "crocodile",
	"darkmantle", "deer", "deva", "djinni", "doppelganger", "dretch", "drider",
	"drow", "dryad", "duergar", "eagle", "efreeti", "elephant", "erinyes",
	"ettercap", "ettin", "frog", "gargoyle", "ghast", "ghost", "ghoul",
	"glabrezu", "gnoll", "goat", "goblin", "gorgon", "grick", "griffon",
	"grimlock", "gynosphinx", "harpy", "hawk", "hezrou", "hippogriff",
	"hobgoblin", "homunculus", "hydra", "hyena", "jackal", "kobold", "kraken",
	"lamia", "lemure", "lich", "lion", "lizard", "lizardfolk", "magmin",
	"mammoth", "manticore", "marilith", "mastiff", "medusa", "merfolk",
	"merrow", "mimic", "minotaur", "mule", "mummy", "nalfeshnee", "nightmare",
	"octopus", "ogre", "otyugh", "owlbear", "panther", "pegasus", "planetar",
	"plesiosaurus", "pony", "pseudodragon", "quasit", "quipper", "rakshasa",
	"raven", "remorhaz", "rhinoceros", "roper", "sahuagin", "salamander",
	"satyr", "scorpion", "shadow", "shrieker", "skeleton", "solar", "specter",
	"spider", "sprite", "stirge", "tarrasque", "tiger", "treant", "triceratops",
	"troll", "unicorn", "vrock", "vulture", "warhorse", "weasel", "wight",
	"wolf", "worg", "wraith", "wyvern", "xorn", "zombie",
}
