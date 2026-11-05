package drivers

import "path/filepath"

// laravelDriver: artisan + public/index.php.
type laravelDriver struct{}

func (laravelDriver) Name() string { return "laravel" }

func (laravelDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "artisan")) && isFile(filepath.Join(dir, "public", "index.php"))
}

func (d laravelDriver) Plan(dir string) ServePlan {
	return phpPlan(d.Name(), dir, filepath.Join(dir, "public"))
}

// statamicDriver: please + public/index.php. Statamic apps are Laravel apps
// and also ship artisan, so in the plan's detection order they are reported
// as "laravel"; only a project with please but no artisan reaches this
// driver. The serve plans are identical, so nothing is served differently.
type statamicDriver struct{}

func (statamicDriver) Name() string { return "statamic" }

func (statamicDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "please")) && isFile(filepath.Join(dir, "public", "index.php"))
}

func (d statamicDriver) Plan(dir string) ServePlan {
	return phpPlan(d.Name(), dir, filepath.Join(dir, "public"))
}

// craftDriver: craft + web/index.php.
type craftDriver struct{}

func (craftDriver) Name() string { return "craft" }

func (craftDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "craft")) && isFile(filepath.Join(dir, "web", "index.php"))
}

func (d craftDriver) Plan(dir string) ServePlan {
	return phpPlan(d.Name(), dir, filepath.Join(dir, "web"))
}

// genericDriver: any project with public/index.php.
type genericDriver struct{}

func (genericDriver) Name() string { return "generic" }

func (genericDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "public", "index.php"))
}

func (d genericDriver) Plan(dir string) ServePlan {
	return phpPlan(d.Name(), dir, filepath.Join(dir, "public"))
}

// staticDriver: index.html in the project root, else in public/.
type staticDriver struct{}

func (staticDriver) Name() string { return "static" }

func (staticDriver) Detect(dir string) bool {
	return isFile(filepath.Join(dir, "index.html")) || isFile(filepath.Join(dir, "public", "index.html"))
}

func (d staticDriver) Plan(dir string) ServePlan {
	docroot := dir
	if !isFile(filepath.Join(dir, "index.html")) && isFile(filepath.Join(dir, "public", "index.html")) {
		docroot = filepath.Join(dir, "public")
	}
	return ServePlan{
		Driver:     d.Name(),
		Docroot:    docroot,
		PHP:        false,
		TryFiles:   []string{tokenPath, tokenPath + "/index.html", tokenPath + ".html", "=404"},
		IndexFiles: []string{"index.html"},
		Deny:       denyRules(dir, docroot),
		Allow:      allowRules(),
	}
}
