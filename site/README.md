# Site Updates

We use GitHub pages to host the website that includes all of the OPA documentation. In order to update the website, you need to have write permission on the open-policy-agent/opa repository.

You also need to have [Jekyll](http://jekyllrb.com) installed to build the site. The instructions below install it
for you. Assuming you have Ruby installed, all you should need to do is run:

```
gem install --user-install jekyll \
    autoprefixer-rails jekyll-assets jekyll-contentblocks jekyll-minifier
```

Changing the documentation on the live site requires two separate merges:

1. Changes to documentation source files must be merged into the master branch.

1. Website artifacts must be re-built and merged into the gh-pages branch.

## Write and View Documentation Locally

1. Start webserver on your local machine:

    ```
    cd opa/site
    jekyll serve .
    ```

1. View docs in the browser.  After you make local changes, you can just refresh your browser to the latest version.

    ```
    http://localhost:4000/
    ```

Once you are happy with the changes, commit them and open a Pull Request against master.

## Update Website Artifacts

To update the live website, perform the following steps:

1. Obtain a fresh copy of the repository and build the site.

    ```
    git clone git@github.com:open-policy-agent/opa.git
    cd opa/site
    jekyll build .
    tar czvf ~/site.tar.gz -C _site .
    ```

    > If you are updating the website as part of a release, the site content
    > will have been built by the `make release` command so this step can be
    > skipped.

1. Checkout the gh-pages branch and overlay the new site content:

    ```
    git checkout gh-pages
    tar zxvf ~/site.tar.gz
    git commit -a
    ```

1. Push the gh-pages branch back to GitHub:

    ```
    git push origin gh-pages
    ```

## REST API Examples

The REST API specification contains examples that are generated manually by running `./_scripts/rest-examples/gen-examples.sh`. This script will launch OPA and execute a series of API calls to produce output can be copied into the specification.
